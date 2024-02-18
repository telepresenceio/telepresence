// Package docker contains the functions necessary to start or discover a Telepresence daemon running in a docker container.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerClient "github.com/docker/docker/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtime2 "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/kubeauth"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	telepresenceImage = "telepresence" // TODO: Point to docker.io/datawire and make it configurable
	TpCache           = "/root/.cache/telepresence"
	dockerTpConfig    = "/root/.config/telepresence"
	dockerTpLog       = "/root/.cache/telepresence/logs"
)

var ClientImageName = telepresenceImage //nolint:gochecknoglobals // extension point

// ClientImage returns the fully qualified name of the docker image that corresponds to
// the version of the current executable.
func ClientImage(ctx context.Context) string {
	images := client.GetConfig(ctx).Images()
	img := images.ClientImage(ctx)
	if img == "" {
		registry := images.Registry(ctx)
		img = registry + "/" + ClientImageName + ":" + strings.TrimPrefix(version.Version, "v")
	}
	return img
}

// DaemonOptions returns the options necessary to pass to a docker run when starting a daemon container.
func DaemonOptions(ctx context.Context, daemonID *daemon.Identifier) ([]string, *net.TCPAddr, error) {
	as, err := dnet.FreePortsTCP(1)
	if err != nil {
		return nil, nil, err
	}
	addr := as[0]
	opts := []string{
		"--name", daemonID.ContainerName(),
		"--network", "telepresence",
		"--cap-add", "NET_ADMIN",
		"--sysctl", "net.ipv6.conf.all.disable_ipv6=0",
		"--device", "/dev/net/tun:/dev/net/tun",
		"-e", fmt.Sprintf("TELEPRESENCE_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("TELEPRESENCE_GID=%d", os.Getgid()),
		"-p", fmt.Sprintf("%s:%d", addr, addr.Port),
		"-v", fmt.Sprintf("%s:%s:ro", filelocation.AppUserConfigDir(ctx), dockerTpConfig),
		"-v", fmt.Sprintf("%s:%s", filelocation.AppUserCacheDir(ctx), TpCache),
		"-v", fmt.Sprintf("%s:%s", filelocation.AppUserLogDir(ctx), dockerTpLog),
	}
	cr := daemon.GetRequest(ctx)
	for _, ep := range cr.ExposedPorts {
		opts = append(opts, "-p", ep)
	}
	if cr.Hostname != "" {
		opts = append(opts, "--hostname", cr.Hostname)
	}
	if runtime.GOOS == "linux" {
		opts = append(opts, "--add-host", "host.docker.internal:host-gateway")
	}
	env := client.GetEnv(ctx)
	if env.ScoutDisable {
		opts = append(opts, "-e", "SCOUT_DISABLE=1")
	}
	return opts, addr, nil
}

// DaemonArgs returns the arguments to pass to a docker run when starting a container daemon.
func DaemonArgs(daemonID *daemon.Identifier, port int) []string {
	return []string{
		"connector-foreground",
		"--name", "docker-" + daemonID.String(),
		"--address", fmt.Sprintf(":%d", port),
		"--embed-network",
	}
}

// ConnectDaemon connects to a containerized daemon at the given address.
func ConnectDaemon(ctx context.Context, address string) (conn *grpc.ClientConn, err error) {
	// Assume that the user daemon is running and connect to it using the given address instead of using a socket.
	for i := 1; ; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, err = grpc.DialContext(ctx, address,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithNoProxy(),
			grpc.WithBlock(),
			grpc.FailOnNonTempDialError(true))
		if err != nil {
			if i < 10 {
				// It's likely that we were too quick. Let's take a nap and try again
				time.Sleep(time.Duration(i*50) * time.Millisecond)
				continue
			}
			return nil, err
		}
		return conn, nil
	}
}

const (
	kubeAuthPortFile = kubeauth.CommandName + ".port"
	kubeConfigs      = "kube"
)

func readPortFile(ctx context.Context, portFile string, configFiles []string) (uint16, error) {
	pb, err := os.ReadFile(portFile)
	if err != nil {
		return 0, err
	}
	var p kubeauth.PortFile
	err = json.Unmarshal(pb, &p)
	if err == nil {
		if p.Kubeconfig == strings.Join(configFiles, string(filepath.ListSeparator)) {
			return uint16(p.Port), nil
		}
		dlog.Debug(ctx, "kubeconfig used by kubeauth is no longer valid")
	}
	if err := os.Remove(portFile); err != nil {
		return 0, err
	}
	return 0, os.ErrNotExist
}

func startAuthenticatorService(ctx context.Context, portFile string, kubeFlags map[string]string, configFiles []string) (uint16, error) {
	// remove any stale port file
	_ = os.Remove(portFile)

	args := make([]string, 0, 4+len(kubeFlags)*2)
	args = append(args, client.GetExe(ctx), kubeauth.CommandName, "--portfile", portFile)
	var err error
	if args, err = client.AppendKubeFlags(kubeFlags, args); err != nil {
		return 0, err
	}
	if err := proc.StartInBackground(true, args...); err != nil {
		return 0, err
	}

	// Wait for the new port file to emerge
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		dtime.SleepWithContext(ctx, 10*time.Millisecond)
		port, err := readPortFile(ctx, portFile, configFiles)
		if err != nil {
			if !os.IsNotExist(err) {
				return 0, err
			}
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf(`timeout while waiting for "%s %s" to create a port file`, client.GetExe(ctx), kubeauth.CommandName)
}

func ensureAuthenticatorService(ctx context.Context, kubeFlags map[string]string, configFiles []string) (uint16, error) {
	portFile := filepath.Join(filelocation.AppUserCacheDir(ctx), kubeAuthPortFile)
	st, err := os.Stat(portFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return 0, err
		}
	} else if st.ModTime().Add(kubeauth.PortFileStaleTime).After(time.Now()) {
		port, err := readPortFile(ctx, portFile, configFiles)
		if err == nil {
			dlog.Debug(ctx, "kubeauth service found alive and valid")
			return port, nil
		}
		if !os.IsNotExist(err) {
			return 0, err
		}
	}
	return startAuthenticatorService(ctx, portFile, kubeFlags, configFiles)
}

func enableK8SAuthenticator(ctx context.Context, daemonID *daemon.Identifier) error {
	cr := daemon.GetRequest(ctx)
	if cr.Implicit {
		return nil
	}
	if kkf, ok := cr.ContainerKubeFlagOverrides["kubeconfig"]; ok && strings.HasPrefix(kkf, TpCache) {
		// Been there, done that
		return nil
	}
	config, err := patcher.CreateExternalKubeConfig(ctx, cr.KubeFlags,
		func(configFiles []string) (string, string, error) {
			port, err := ensureAuthenticatorService(ctx, cr.KubeFlags, configFiles)
			if err != nil {
				return "", "", err
			}

			// The telepresence command that will run in order to retrieve the credentials from the authenticator service
			// will run in a container, so the first argument must be a path that finds the telepresence executable and
			// the second must be an address that will find the host's port, not the container's localhost.
			return "telepresence", fmt.Sprintf("host.docker.internal:%d", port), nil
		},
		func(config *api.Config) error {
			return handleLocalK8s(ctx, daemonID, config)
		})
	if err != nil {
		return err
	}
	patcher.AnnotateConnectRequest(&cr.ConnectRequest, TpCache, config.CurrentContext)
	return err
}

// handleLocalK8s checks if the cluster is using a well known provider (currently minikube or kind)
// and if so, ensures that the daemon container is connected to its network.
func handleLocalK8s(ctx context.Context, daemonID *daemon.Identifier, config *api.Config) error {
	cc := config.Contexts[config.CurrentContext]
	cl := config.Clusters[cc.Cluster]
	server, err := url.Parse(cl.Server)
	if err != nil {
		return err
	}
	host, portStr, err := net.SplitHostPort(server.Host)
	if err != nil {
		// Host doesn't have a port, so it's not a local k8s.
		return nil
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		if host == "localhost" {
			addr = netip.AddrFrom4([4]byte{127, 0, 0, 1})
			err = nil
		}
	}
	if err != nil {
		return nil
	}
	isMinikube := false
	if ex, ok := cl.Extensions["cluster_info"].(*runtime2.Unknown); ok {
		var data map[string]any
		isMinikube = json.Unmarshal(ex.Raw, &data) == nil && data["provider"] == "minikube.sigs.k8s.io"
	}
	if !(addr.IsLoopback() || isMinikube) {
		return nil
	}

	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return err
	}
	addrPort := netip.AddrPortFrom(addr, uint16(port))

	// Let's check if we have a container with port bindings for the
	// given addrPort that is a known k8sapi provider
	cli, err := GetClient(ctx)
	if err != nil {
		return err
	}
	cjs := runningContainers(ctx, cli)

	var hostPort netip.AddrPort
	var network string
	if isMinikube {
		hostPort, network = detectMinikube(ctx, cjs, addrPort, cc.Cluster)
	} else {
		hostPort, network = detectKind(ctx, cjs, addrPort)
	}
	if hostPort.IsValid() {
		dlog.Debugf(ctx, "hostPort %s, network %s", hostPort, network)
		server.Host = hostPort.String()
		cl.Server = server.String()
	}
	if network != "" {
		dcName := daemonID.ContainerName()
		dlog.Debugf(ctx, "Connecting network %s to container %s", network, dcName)
		if err = cli.NetworkConnect(ctx, network, dcName, nil); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				dlog.Debugf(ctx, "failed to connect network %s to container %s: %v", network, dcName, err)
			}
		}
	}
	return nil
}

// LaunchDaemon ensures that the image returned by ClientImage exists by calling PullImage. It then uses the
// options DaemonOptions and DaemonArgs to start the image, and finally connectDaemon to connect to it. A
// successful start yields a cache.Info entry in the cache.
func LaunchDaemon(ctx context.Context, daemonID *daemon.Identifier) (conn *grpc.ClientConn, err error) {
	if proc.RunningInContainer() {
		return nil, errors.New("unable to start a docker container from within a container")
	}
	image := ClientImage(ctx)
	if err = PullImage(ctx, image); err != nil {
		return nil, err
	}

	if err = EnsureNetwork(ctx, "telepresence"); err != nil {
		return nil, err
	}
	opts, addr, err := DaemonOptions(ctx, daemonID)
	if err != nil {
		return nil, errcat.NoDaemonLogs.New(err)
	}
	args := DaemonArgs(daemonID, addr.Port)

	allArgs := make([]string, 0, len(opts)+len(args)+4)
	allArgs = append(allArgs,
		"run",
		"--rm",
		"-d",
	)
	allArgs = append(allArgs, opts...)
	allArgs = append(allArgs, image)
	allArgs = append(allArgs, args...)
	stopAttempted := false
	for i := 1; ; i++ {
		_, err = tryLaunch(ctx, daemonID, addr.Port, allArgs)
		if err != nil {
			if !strings.Contains(err.Error(), "already in use by container") {
				return nil, errcat.NoDaemonLogs.New(err)
			}
			// This may happen if the daemon has died (and hence, we never discovered it), but
			// the container still hasn't died. Let's sleep for a short while and retry.
			if i < 6 {
				dtime.SleepWithContext(ctx, time.Duration(i)*200*time.Millisecond)
				continue
			}
			if stopAttempted {
				return nil, err
			}
			// Container is still alive. Try and stop it.
			stopContainer(ctx, daemonID)
			stopAttempted = true
			i = 1
			continue
		}
		break
	}
	if err = enableK8SAuthenticator(ctx, daemonID); err != nil {
		return nil, err
	}
	if conn, err = ConnectDaemon(ctx, addr.String()); err != nil {
		return nil, err
	}
	return conn, nil
}

// containerPort returns the port that the container uses internally to expose the given
// addrPort on the host. Zero is returned when the addrPort is not found among
// the container's port bindings.
// The additional bool is true if the host address is IPv6.
func containerPort(addrPort netip.AddrPort, ns *types.NetworkSettings) (port uint16, isIPv6 bool) {
	for portDef, bindings := range ns.Ports {
		if portDef.Proto() != "tcp" {
			continue
		}
		for _, binding := range bindings {
			addr, err := netip.ParseAddr(binding.HostIP)
			if err != nil {
				continue
			}
			pn, err := strconv.ParseUint(binding.HostPort, 10, 16)
			if err != nil {
				continue
			}
			if netip.AddrPortFrom(addr, uint16(pn)) == addrPort {
				return uint16(portDef.Int()), addr.Is6()
			}
		}
	}
	return 0, false
}

// runningContainers returns the inspect data for all containers with status=running.
func runningContainers(ctx context.Context, cli dockerClient.APIClient) []types.ContainerJSON {
	cl, err := cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.KeyValuePair{Key: "status", Value: "running"}),
	})
	if err != nil {
		dlog.Errorf(ctx, "failed to list containers: %v", err)
		return nil
	}
	cjs := make([]types.ContainerJSON, 0, len(cl))
	for _, cn := range cl {
		cj, err := cli.ContainerInspect(ctx, cn.ID)
		if err != nil {
			dlog.Errorf(ctx, "container inspect on %v failed: %v", cn.Names, err)
		} else {
			cjs = append(cjs, cj)
		}
	}
	return cjs
}

func localAddr(ctx context.Context, cnID, nwID string, isIPv6 bool) (addr netip.Addr, err error) {
	cli, err := GetClient(ctx)
	if err != nil {
		return addr, err
	}
	nw, err := cli.NetworkInspect(ctx, nwID, types.NetworkInspectOptions{})
	if err != nil {
		return addr, err
	}
	if cn, ok := nw.Containers[cnID]; ok {
		// These aren't IP-addresses at all. They are prefixes!
		var prefix string
		if isIPv6 {
			prefix = cn.IPv6Address
		} else {
			prefix = cn.IPv4Address
		}
		ap, err := netip.ParsePrefix(prefix)
		if err == nil {
			addr = ap.Addr()
		}
	}
	return addr, err
}

// detectMinikube returns the container IP:port for the given hostAddrPort for a container where the
// "name.minikube.sigs.k8s.io" label is equal to the given cluster name.
// Returns the internal IP:port for the given hostAddrPort and the name of a network that makes the
// IP available.
func detectMinikube(ctx context.Context, cns []types.ContainerJSON, hostAddrPort netip.AddrPort, clusterName string) (netip.AddrPort, string) {
	for _, cn := range cns {
		if cfg, ns := cn.Config, cn.NetworkSettings; cfg != nil && ns != nil && cfg.Labels["name.minikube.sigs.k8s.io"] == clusterName {
			if port, isIPv6 := containerPort(hostAddrPort, ns); port != 0 {
				for networkName, network := range ns.Networks {
					addr, err := localAddr(ctx, cn.ID, network.NetworkID, isIPv6)
					if err != nil {
						dlog.Error(ctx, err)
						break
					}
					return netip.AddrPortFrom(addr, port), networkName
				}
			}
		}
	}
	return netip.AddrPort{}, ""
}

// detectKind returns the container hostname:port for the given hostAddrPort for a container where the
// "io.x-k8s.kind.role" label is equal to "control-plane".
// Returns the internal hostname:port for the given hostAddrPort and the name of a network that makes the
// hostname available.
func detectKind(ctx context.Context, cns []types.ContainerJSON, hostAddrPort netip.AddrPort) (netip.AddrPort, string) {
	for _, cn := range cns {
		if cfg, ns := cn.Config, cn.NetworkSettings; cfg != nil && ns != nil && cfg.Labels["io.x-k8s.kind.role"] == "control-plane" {
			if port, isIPv6 := containerPort(hostAddrPort, ns); port != 0 {
				for n, nw := range ns.Networks {
					for _, alias := range nw.Aliases {
						if strings.HasSuffix(alias, "-control-plane") {
							addr, err := localAddr(ctx, cn.ID, nw.NetworkID, isIPv6)
							if err != nil {
								dlog.Error(ctx, err)
								break
							}
							return netip.AddrPortFrom(addr, port), n
						}
					}
				}
			}
		}
	}
	return netip.AddrPort{}, ""
}

func stopContainer(ctx context.Context, daemonID *daemon.Identifier) {
	args := []string{"stop", daemonID.ContainerName()}
	dlog.Debug(ctx, shellquote.ShellString("docker", args))
	if _, err := proc.CaptureErr(dexec.CommandContext(ctx, "docker", args...)); err != nil {
		dlog.Warn(ctx, err)
	}
}

func tryLaunch(ctx context.Context, daemonID *daemon.Identifier, port int, args []string) (string, error) {
	stdErr := bytes.Buffer{}
	stdOut := bytes.Buffer{}
	dlog.Debug(ctx, shellquote.ShellString("docker", args))
	cmd := proc.CommandContext(ctx, "docker", args...)
	cmd.DisableLogging = true
	cmd.Stderr = &stdErr
	cmd.Stdout = &stdOut
	if err := cmd.Run(); err != nil {
		errStr := strings.TrimSpace(stdErr.String())
		if errStr == "" {
			errStr = err.Error()
		}
		return "", fmt.Errorf("launch of daemon container failed: %s", errStr)
	}
	cid := strings.TrimSpace(stdOut.String())
	cr := daemon.GetRequest(ctx)
	return cid, daemon.SaveInfo(ctx,
		&daemon.Info{
			Options:      map[string]string{"cid": cid},
			InDocker:     true,
			DaemonPort:   port,
			Name:         daemonID.Name,
			KubeContext:  daemonID.KubeContext,
			Namespace:    daemonID.Namespace,
			ExposedPorts: cr.ExposedPorts,
			Hostname:     cr.Hostname,
		}, daemonID.InfoFileName())
}

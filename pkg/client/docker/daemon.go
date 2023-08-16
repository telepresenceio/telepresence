// Package docker contains the functions necessary to start or discover a Telepresence daemon running in a docker container.
package docker

import (
	"bytes"
	"context"
	"encoding/csv"
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
	"github.com/docker/docker/api/types/filters"
	dockerClient "github.com/docker/docker/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtime2 "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/kubeauth"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/slice"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	telepresenceImage   = "telepresence" // TODO: Point to docker.io/datawire and make it configurable
	dockerTpCache       = "/root/.cache/telepresence"
	dockerTpConfig      = "/root/.config/telepresence"
	dockerTpLog         = "/root/.cache/telepresence/logs"
	containerNamePrefix = "tp-"
)

// ClientImage returns the fully qualified name of the docker image that corresponds to
// the version of the current executable.
func ClientImage(ctx context.Context) string {
	registry := client.GetConfig(ctx).Images().Registry(ctx)
	return registry + "/" + telepresenceImage + ":" + strings.TrimPrefix(version.Version, "v")
}

// DaemonOptions returns the options necessary to pass to a docker run when starting a daemon container.
func DaemonOptions(ctx context.Context, name string) ([]string, *net.TCPAddr, error) {
	as, err := dnet.FreePortsTCP(1)
	if err != nil {
		return nil, nil, err
	}
	addr := as[0]
	port := addr.Port
	opts := []string{
		"--name", SafeContainerName(containerNamePrefix + name),
		"--network", "telepresence",
		"--cap-add", "NET_ADMIN",
		"--device", "/dev/net/tun:/dev/net/tun",
		"-e", fmt.Sprintf("TELEPRESENCE_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("TELEPRESENCE_GID=%d", os.Getgid()),
		"-p", fmt.Sprintf("%s:%d", addr, port),
		"-v", fmt.Sprintf("%s:%s:ro", filelocation.AppUserConfigDir(ctx), dockerTpConfig),
		"-v", fmt.Sprintf("%s:%s", filelocation.AppUserCacheDir(ctx), dockerTpCache),
		"-v", fmt.Sprintf("%s:%s", filelocation.AppUserLogDir(ctx), dockerTpLog),
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

// SafeContainerName returns a string that can safely be used as an argument
// to docker run --name. Only characters [a-zA-Z0-9][a-zA-Z0-9_.-] are allowed.
// Others are replaced by an underscore, or if it's the very first character,
// by the character 'a'.
func SafeContainerName(name string) string {
	n := strings.Builder{}
	for i, c := range name {
		switch {
		case (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			n.WriteByte(byte(c))
		case i > 0 && (c == '_' || c == '.' || c == '-'):
			n.WriteByte(byte(c))
		case i > 0:
			n.WriteByte('_')
		default:
			n.WriteByte('a')
		}
	}
	return n.String()
}

// DaemonArgs returns the arguments to pass to a docker run when starting a container daemon.
func DaemonArgs(name string, port int) []string {
	return []string{
		"connector-foreground",
		"--name", SafeContainerName("docker-" + name),
		"--address", fmt.Sprintf(":%d", port),
		"--embed-network",
	}
}

// DiscoverDaemon searches the daemon cache for an entry corresponding to the given name. A connection
// to that daemon is returned if such an entry is found.
func DiscoverDaemon(ctx context.Context, name string) (conn *grpc.ClientConn, err error) {
	port, err := cache.DaemonPortForName(ctx, name)
	if err != nil {
		return nil, err
	}
	var addr string
	if proc.RunningInContainer() {
		// Containers use the daemon container DNS name
		addr = fmt.Sprintf("%s:%d", name, port)
	} else {
		// The host relies on that the daemon has exposed a port to localhost
		addr = fmt.Sprintf(":%d", port)
	}
	return connectDaemon(ctx, addr)
}

// connectDaemon connects to a daemon at the given address.
func connectDaemon(ctx context.Context, address string) (conn *grpc.ClientConn, err error) {
	if err = enableK8SAuthenticator(ctx); err != nil {
		return nil, err
	}
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

const kubeAuthPortFile = kubeauth.CommandName + ".port"

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

func appendKubeFlags(kubeFlags map[string]string, args []string) ([]string, error) {
	for k, v := range kubeFlags {
		switch k {
		case "as-group":
			// Multi valued
			r := csv.NewReader(strings.NewReader(v))
			gs, err := r.Read()
			if err != nil {
				return nil, err
			}
			for _, g := range gs {
				args = append(args, "--"+k, g)
			}
		case "disable-compression", "insecure-skip-tls-verify":
			// Boolean with false default.
			if v != "false" {
				args = append(args, "--"+k)
			}
			continue
		default:
			// Kubeconfig flags which are not env vars should not be propagated to the authenticator.
			if !slice.Contains(client.EnvVarOnlyKubeFlags, k) {
				args = append(args, "--"+k, v)
			}
		}
	}
	return args, nil
}

func startAuthenticatorService(ctx context.Context, portFile string, kubeFlags map[string]string, configFiles []string) (uint16, error) {
	// remove any stale port file
	_ = os.Remove(portFile)

	args := make([]string, 0, 4+len(kubeFlags)*2)
	args = append(args, client.GetExe(), kubeauth.CommandName, "--portfile", portFile)
	var err error
	if args, err = appendKubeFlags(kubeFlags, args); err != nil {
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
	return 0, fmt.Errorf(`timeout while waiting for "%s %s" to create a port file`, client.GetExe(), kubeauth.CommandName)
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

func enableK8SAuthenticator(ctx context.Context) error {
	cr := daemon.GetRequest(ctx)
	if kkf, ok := cr.KubeFlags["kubeconfig"]; ok && strings.HasPrefix(kkf, dockerTpCache) {
		// Been there, done that
		return nil
	}
	dlog.Debugf(ctx, "kubeflags = %v", cr.KubeFlags)
	configFlags, err := client.ConfigFlags(cr.KubeFlags)
	if err != nil {
		return err
	}
	loader := configFlags.ToRawKubeConfigLoader()
	configFiles := loader.ConfigAccess().GetLoadingPrecedence()
	dlog.Debugf(ctx, "config = %v", configFiles)
	config, err := loader.RawConfig()
	if err != nil {
		return err
	}

	// Minify the config so that we only deal with the current context.
	if cx := configFlags.Context; cx != nil && *cx != "" {
		config.CurrentContext = *cx
	}
	if err = api.MinifyConfig(&config); err != nil {
		return err
	}
	dlog.Debugf(ctx, "context = %v", config.CurrentContext)

	// Minify guarantees that the CurrentContext is set, but not that it has a cluster
	cc := config.Contexts[config.CurrentContext]
	if cc.Cluster == "" {
		return fmt.Errorf("current context %q has no cluster", config.CurrentContext)
	}

	if patcher.NeedsStubbedExec(&config) {
		port, err := ensureAuthenticatorService(ctx, cr.KubeFlags, configFiles)
		if err != nil {
			return err
		}
		// Replace any auth exec with a stub
		addr := fmt.Sprintf("host.docker.internal:%d", port)
		if err := patcher.ReplaceAuthExecWithStub(&config, addr); err != nil {
			return err
		}
	}

	// Ensure that all certs are embedded instead of reachable using a path
	if err = api.FlattenConfig(&config); err != nil {
		return err
	}

	// Store the file using its context name under the <telepresence cache>/kube directory
	const kubeConfigs = "kube"
	kubeConfigFile := config.CurrentContext
	kubeConfigFile = strings.ReplaceAll(kubeConfigFile, "/", "-")
	kubeConfigDir := filepath.Join(filelocation.AppUserCacheDir(ctx), kubeConfigs)
	if err = os.MkdirAll(kubeConfigDir, 0o700); err != nil {
		return err
	}
	err = handleLocalK8s(ctx, cc.Cluster, config.Clusters[cc.Cluster])
	if err != nil {
		dlog.Errorf(ctx, "unable to handle local K8s: %v", err)
	}

	if err = clientcmd.WriteToFile(config, filepath.Join(kubeConfigDir, kubeConfigFile)); err != nil {
		return err
	}

	// Concatenate using "/". This will be used in linux
	cr.KubeFlags["kubeconfig"] = fmt.Sprintf("%s/%s/%s", dockerTpCache, kubeConfigs, kubeConfigFile)
	return nil
}

// handleLocalK8s checks if the cluster is using a well known provider (currently minikube or kind)
// and ensures that the service is modified to access the docker internal address instead of an
// address available on the host.
func handleLocalK8s(ctx context.Context, clusterName string, cl *api.Cluster) error {
	isKind := strings.HasPrefix(clusterName, "kind-")
	isMinikube := false
	if !isKind {
		if ex, ok := cl.Extensions["cluster_info"].(*runtime2.Unknown); ok {
			var data map[string]any
			isMinikube = json.Unmarshal(ex.Raw, &data) == nil && data["provider"] == "minikube.sigs.k8s.io"
		}
	}
	if !(isKind || isMinikube) {
		return nil
	}

	server, err := url.Parse(cl.Server)
	if err != nil {
		return err
	}
	host, portStr, err := net.SplitHostPort(server.Host)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		if host == "localhost" {
			addr = netip.AddrFrom4([4]byte{127, 0, 0, 1})
		}
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return err
	}

	addrPort := netip.AddrPortFrom(addr, uint16(port))

	// Let's check if we have a container with port bindings for the
	// given addrPort that is a known k8sapi provider
	cli := GetClient(ctx)
	cjs := runningContainers(ctx, cli)

	var hostPort, network string
	if isKind {
		hostPort, network = detectKind(cjs, addrPort)
	} else if isMinikube {
		hostPort, network = detectMinikube(cjs, addrPort, clusterName)
	}
	if hostPort != "" {
		server.Host = hostPort
		cl.Server = server.String()
	}
	if network != "" {
		dcName := SafeContainerName(containerNamePrefix + clusterName)
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
// successful start yields a cache.DaemonInfo entry in the cache.
func LaunchDaemon(ctx context.Context, name string) (conn *grpc.ClientConn, err error) {
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
	opts, addr, err := DaemonOptions(ctx, name)
	if err != nil {
		return nil, errcat.NoDaemonLogs.New(err)
	}
	args := DaemonArgs(name, addr.Port)

	allArgs := make([]string, 0, len(opts)+len(args)+4)
	allArgs = append(allArgs,
		"run",
		"--rm",
		"-d",
	)
	allArgs = append(allArgs, opts...)
	allArgs = append(allArgs, image)
	allArgs = append(allArgs, args...)
	for i := 1; ; i++ {
		_, err = tryLaunch(ctx, addr.Port, name, allArgs)
		if err != nil {
			if i < 6 && strings.Contains(err.Error(), "already in use by container") {
				// This may happen if the daemon has died (and hence, we never discovered it), but
				// the container still hasn't died. Let's sleep for a short while and retry.
				dtime.SleepWithContext(ctx, time.Duration(i)*200*time.Millisecond)
				continue
			}
			return nil, errcat.NoDaemonLogs.New(err)
		}
		break
	}
	return connectDaemon(ctx, addr.String())
}

// containerPort returns the port that the container uses internally to expose the given
// addrPort on the host. An empty string is returned when the addrPort is not found among
// the container's port bindings.
func containerPort(addrPort netip.AddrPort, ns *types.NetworkSettings) string {
	for port, bindings := range ns.Ports {
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
				return port.Port()
			}
		}
	}
	return ""
}

// runningContainers returns the inspect data for all containers with status=running.
func runningContainers(ctx context.Context, cli dockerClient.APIClient) []types.ContainerJSON {
	cl, err := cli.ContainerList(ctx, types.ContainerListOptions{
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

// detectMinikube returns the container IP:port for the given hostAddrPort for a container where the
// "name.minikube.sigs.k8s.io" label is equal to the given cluster name.
// Returns the internal IP:port for the given hostAddrPort and the name of a network that makes the
// IP available.
func detectMinikube(cns []types.ContainerJSON, hostAddrPort netip.AddrPort, clusterName string) (string, string) {
	for _, cn := range cns {
		if cfg, ns := cn.Config, cn.NetworkSettings; cfg != nil && ns != nil && cfg.Labels["name.minikube.sigs.k8s.io"] == clusterName {
			if port := containerPort(hostAddrPort, ns); port != "" {
				for networkName, network := range ns.Networks {
					return net.JoinHostPort(network.IPAddress, port), networkName
				}
			}
		}
	}
	return "", ""
}

// detectKind returns the container hostname:port for the given hostAddrPort for a container where the
// "io.x-k8s.kind.role" label is equal to "control-plane".
// Returns the internal hostname:port for the given hostAddrPort and the name of a network that makes the
// hostname available.
func detectKind(cns []types.ContainerJSON, hostAddrPort netip.AddrPort) (string, string) {
	for _, cn := range cns {
		if cfg, ns := cn.Config, cn.NetworkSettings; cfg != nil && ns != nil && cfg.Labels["io.x-k8s.kind.role"] == "control-plane" {
			if port := containerPort(hostAddrPort, ns); port != "" {
				hostPort := net.JoinHostPort(cfg.Hostname, port)
				for networkName := range ns.Networks {
					return hostPort, networkName
				}
			}
		}
	}
	return "", ""
}

func tryLaunch(ctx context.Context, port int, name string, args []string) (string, error) {
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
	return cid, cache.SaveDaemonInfo(ctx,
		&cache.DaemonInfo{
			Options:     map[string]string{"cid": cid},
			InDocker:    true,
			DaemonPort:  port,
			KubeContext: name,
		}, cache.DaemonInfoFile(name, port))
}

// CancelWhenRmFromCache watches for the file to be removed from the cache, then calls cancel.
func CancelWhenRmFromCache(ctx context.Context, cancel context.CancelFunc, filename string) error {
	return cache.WatchDaemonInfos(ctx, func(ctx context.Context) error {
		exists, err := cache.DaemonInfoExists(ctx, filename)
		if err != nil {
			return err
		}
		if !exists {
			// spec removed from cache, shut down gracefully
			dlog.Infof(ctx, "daemon file %s removed from cache, shutting down gracefully", filename)
			cancel()
		}
		return nil
	}, filename)
}

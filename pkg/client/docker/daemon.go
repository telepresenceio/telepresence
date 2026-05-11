// Package docker contains the functions necessary to start or discover a Telepresence daemon running in a docker container.
package docker

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	dockerClient "github.com/docker/docker/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker/kubeauth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	tpGrpc "github.com/telepresenceio/telepresence/v2/pkg/grpc"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/routing"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	telepresenceImage = "telepresence"
	TpCache           = "/root/.cache/telepresence"
	DockerTpConfig    = "/root/.config/telepresence"
	DockerTpLog       = "/root/.cache/telepresence/logs"
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
func DaemonOptions(ctx context.Context, daemonID *daemon.Identifier, daemonPortOnHost uint16) (opts []string, err error) {
	ipv6, err := UseIPv6(ctx)
	if err != nil {
		return nil, err
	}
	ap := daemonAddr(ipv6)
	opts = []string{
		"--name", daemonID.ContainerName(),
		"--cap-add", "NET_ADMIN",
		"--device", "/dev/net/tun:/dev/net/tun",
		"--pid", "host",
		"-e", fmt.Sprintf("TELEPRESENCE_UID=%d", os.Getuid()),
		"-e", fmt.Sprintf("TELEPRESENCE_GID=%d", os.Getgid()),
		"-p", fmt.Sprintf("%s:%d/tcp", netip.AddrPortFrom(ap, daemonPortOnHost), client.GetConfig(ctx).Grpc().DaemonPort),
		"-v", fmt.Sprintf("%s:%s:ro", filepath.Dir(client.GetConfigFile(ctx)), DockerTpConfig),
		"-v", fmt.Sprintf("%s:%s", filelocation.AppUserCacheDir(ctx), TpCache),
		"-v", fmt.Sprintf("%s:%s", filelocation.AppUserLogDir(ctx), DockerTpLog),
	}
	cr := daemon.GetRequest(ctx)
	for _, ep := range cr.ExposedPorts {
		opts = append(opts, "-p", ep)
	}
	if cr.Hostname != "" {
		opts = append(opts, "--hostname", cr.Hostname)
	}
	opts, err = appendOSSpecificContainerOpts(ctx, opts)
	if err != nil {
		return nil, err
	}
	if ipv6 {
		opts = append(opts,
			"--sysctl", "net.ipv6.conf.all.forwarding=1",
			"--sysctl", "net.ipv6.conf.all.disable_ipv6=0",
		)
	}
	cfg := client.GetConfig(ctx).Docker()
	if cfg.HostGateway != "" && (cfg.AddHostGateway || cfg.HostGateway != client.DefaultHostGateway) {
		opts = append(opts, "--add-host", cfg.HostGateway+":host-gateway")
	}
	return opts, nil
}

// DaemonArgs returns the arguments to pass to a docker run when starting a container daemon.
func DaemonArgs(ctx context.Context, daemonID *daemon.Identifier) []string {
	grpcCfg := client.GetConfig(ctx).Grpc()
	return []string{
		client.UserDaemonName,
		"--config", filepath.Join(DockerTpConfig, filepath.Base(client.GetConfigFile(ctx))),
		"--name", "docker-" + daemonID.String(),
		"--address", fmt.Sprintf(":%d", grpcCfg.DaemonPort),
		"--embed-network",
		"--teleroute-port", strconv.Itoa(int(grpcCfg.TeleroutePort)),
	}
}

// ConnectDaemon connects to a containerized daemon at the given address.
func ConnectDaemon(ctx context.Context, info *daemon.Info) (conn *grpc.ClientConn, err error) {
	ipv6, err := UseIPv6(ctx)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(
		netip.AddrPortFrom(daemonAddr(ipv6), info.DaemonPort).String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithNoProxy())
}

func daemonAddr(ipv6 bool) netip.Addr {
	if ipv6 {
		return netip.IPv6Loopback()
	}
	return netip.AddrFrom4([4]byte{127, 0, 0, 1})
}

const (
	kubeAuthPortFile = client.KubeAuthDaemonName + ".port"
)

type ContainerInfo struct {
	ID   string
	Name string
	Pid  int
	IPv4 netip.Addr
	IPv6 netip.Addr
}

// GetDaemonContainerNetworkInfo checks if the daemon VIF routes any subnets. If it does, then the DNS IP
// assigned to the VIF and the network name of the teleroute network is returned. Otherwise, the method
// returns the daemon's IP on the default bridge as the DNS address and an empty string as the network name.
func GetDaemonContainerNetworkInfo(ctx context.Context) (dns netip.Addr, networkName string, err error) {
	ud := daemon.MustGetUserClient(ctx)
	info := ud.DaemonInfo()
	status, err := ud.Status(ctx, &empty.Empty{})
	if err != nil {
		return dns, "", tpGrpc.FromGRPC(err)
	}

	rootCfg, err := daemon.GetRootClientConfig(status.DaemonStatus)
	if err != nil {
		return dns, "", err
	}

	if len(rootCfg.Routing().Subnets) > 0 {
		xi, err := GetContainerInfo(ctx, info.ContainerID, info.Name)
		if err == nil {
			if xi.IPv4.IsValid() {
				dns = xi.IPv4
			} else {
				dns = xi.IPv6
			}
		} else {
			dns = rootCfg.DNS().VIFAddress.Addr()
		}
		networkName = info.Name
	} else {
		// The daemon doesn't route any subnets because it found that the container already had access
		// to the cluster resources. It's then assumed that other containers will have that too.
		// This means that:
		//
		//   1. The IP of the daemon container is the one assigned to the default bridge network.
		//   2. The IP of the daemon container can act as the DNS IP.
		dns = info.ContainerIP
	}
	return dns, networkName, nil
}

// GetContainerInfo returns the name and process ID of the container with the given ID along with its associated IP in the given network.
func GetContainerInfo(ctx context.Context, cid string, network string) (*ContainerInfo, error) {
	cli, err := GetClient(ctx)
	if err != nil {
		return nil, err
	}
	dcfg := client.GetConfig(ctx).Docker()

	bo := backoff.NewExponentialBackOff()
	bo.MaxInterval = 300 * time.Millisecond
	bo.MaxElapsedTime = time.Second
	var info *ContainerInfo
	err = backoff.Retry(func() error {
		ci, err := cli.ContainerInspect(ctx, cid)
		if err != nil {
			// The container in question no longer exists
			return backoff.Permanent(err)
		}
		var iPv4, iPv6 netip.Addr
		if network != "" {
			ns := ci.NetworkSettings
			if ns == nil {
				return errdefs.ErrNotFound
			}
			tn, ok := ns.Networks[network]
			if ok {
				if dcfg.EnableIPv4 && tn.IPAddress != "" {
					iPv4, err = netip.ParseAddr(tn.IPAddress)
					if err != nil {
						return backoff.Permanent(fmt.Errorf("failed to parse IPAddress of network %q: %w", network, err))
					}
					clog.Debugf(ctx, "container %q has IPv4 address %s in network %q", ci.Name, iPv4, network)
				}
				if dcfg.EnableIPv6 && tn.GlobalIPv6Address != "" {
					iPv6, err = netip.ParseAddr(tn.GlobalIPv6Address)
					if err != nil {
						return backoff.Permanent(fmt.Errorf("failed to parse GlobalIPv6Address of network %q: %w", network, err))
					}
					clog.Debugf(ctx, "container %q has IPv6 address %s in network %q", ci.Name, iPv6, network)
				}
			}
			if !iPv4.IsValid() && !iPv6.IsValid() {
				// retry the operation if this happens
				return fmt.Errorf("container %q has no IP address in network %q: %w", ci.Name, network, errdefs.ErrNotFound)
			}
		}
		info = &ContainerInfo{ID: ci.ID, Pid: ci.State.Pid, IPv4: iPv4, IPv6: iPv6, Name: ci.Name}
		return nil
	}, backoff.WithContext(bo, ctx))
	return info, err
}

func UseIPv6(ctx context.Context) (bool, error) {
	dcfg := client.GetConfig(ctx).Docker()
	if !dcfg.EnableIPv6 {
		return false, nil
	}
	cli, err := GetClient(ctx)
	if err != nil {
		return false, err
	}
	ci, err := cli.NetworkInspect(ctx, "bridge", network.InspectOptions{})
	if err != nil {
		return false, err
	}
	return ci.EnableIPv6, nil
}

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
		clog.Debug(ctx, "kubeconfig used by kubeauth is no longer valid")
	}
	if err := os.Remove(portFile); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("%s: %w", portFile, os.ErrNotExist)
}

func startAuthenticatorService(ctx context.Context, portFile string, kubeFlags map[string]string, configFiles []string) (uint16, error) {
	clog.Debugf(ctx, "Starting authenticator service using portFile %s", portFile)
	// remove any stale port file
	_ = os.Remove(portFile)

	args := make([]string, 0, 6+len(kubeFlags)*2)
	args = append(args, client.GetExe(ctx), client.KubeAuthDaemonName, "--"+global.FlagConfig, client.GetConfigFile(ctx), "--portfile", portFile)
	var err error
	if args, err = k8s.AppendKubeFlags(kubeFlags, args); err != nil {
		return 0, err
	}
	if err := proc.StartInBackground(true, args...); err != nil {
		return 0, err
	}

	// Wait for the new port file to emerge
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		time.Sleep(10 * time.Millisecond)
		port, err := readPortFile(ctx, portFile, configFiles)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return 0, err
			}
			continue
		}
		clog.Debugf(ctx, "Authenticator service started on port %d", port)
		return port, nil
	}
	return 0, fmt.Errorf(`timeout while waiting for "%s %s" to create a port file`, client.GetExe(ctx), client.KubeAuthDaemonName)
}

func ensureAuthenticatorService(ctx context.Context, kubeFlags map[string]string, configFiles []string) (uint16, error) {
	portFile := filepath.Join(filelocation.AppUserCacheDir(ctx), kubeAuthPortFile)
	st, err := os.Stat(portFile)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
	} else if st.ModTime().Add(kubeauth.PortFileStaleTime).After(time.Now()) {
		port, err := readPortFile(ctx, portFile, configFiles)
		if err == nil {
			clog.Debug(ctx, "kubeauth service found alive and valid")
			return port, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
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
	if len(cr.KubeconfigData) > 0 {
		// Been there, done that
		return nil
	}
	loader, err := k8s.ConfigLoader(ctx, cr.KubeFlags, cr.KubeconfigData)
	if err != nil {
		return err
	}
	content, err := patcher.CreateExternalKubeConfig(ctx, loader, cr.KubeFlags["context"],
		func(configFiles []string) (string, string, string, error) {
			port, err := ensureAuthenticatorService(ctx, cr.KubeFlags, configFiles)
			if err != nil {
				clog.Errorf(ctx, "failed to start k8s authenticator service: %v", err)
				return "", "", "", err
			}

			// The telepresence command that will run in order to retrieve the credentials from the authenticator service
			// will run in a container, so the first argument must be a path that finds the telepresence executable and
			// the second must be an address that will find the host's port, not the container's localhost. The host
			// in this case is the client performing the authentication (as opposed to the Docker VM, when one is used).
			cfg := client.GetConfig(ctx).Docker()
			kubeAuthHost := cfg.HostGateway
			if kubeAuthHost == "" {
				r, err := routing.DefaultRoute(ctx)
				if err != nil {
					return "", "", "", err
				}
				kubeAuthHost = r.LocalIP.String()
			}
			return "telepresence", iputil.JoinHostPort(kubeAuthHost, port), filepath.Join(DockerTpConfig, filepath.Base(client.GetConfigFile(ctx))), nil
		},
		func(config *api.Config) error {
			return handleLocalK8s(ctx, daemonID, config)
		})
	if err != nil {
		return err
	}
	cr.KubeconfigData = content
	return nil
}

// handleLocalK8s checks if the cluster is using a well-known provider (currently minikube or kind)
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
		if host != "localhost" {
			// Address is not a valid IP address, so it's not a local k8s. Docker can't make
			// containers available to the host via DNS.
			return nil
		}
		addr = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}

	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil
	}
	addrPort := netip.AddrPortFrom(addr, uint16(port))

	// Let's check if we have a container with port bindings for the
	// given addrPort that is a known k8sapi provider
	cli, err := GetClient(ctx)
	if err != nil {
		return err
	}
	cjs := runningContainers(ctx, cli)

	hostPort, nw := detectControlPlane(ctx, cli, cjs, addrPort)
	if hostPort.IsValid() {
		server.Host = hostPort.String()
		cl.Server = server.String()
	} else if addrPort.Addr().IsLoopback() {
		// We're running in a container, but apparently the control-plan isn't. Since we can't use the host's loopback interface directly,
		// the best we can do here is to use the "host.docker.internal" (or whatever alias the user has configured for the GatewayHost) and
		// hope that the server's certificate is configured to accept connections from that address.
		server.Host = iputil.JoinHostPort(client.GetConfig(ctx).Docker().HostGateway, addrPort.Port())
		clog.Debugf(ctx, "Connecting to host's %s via alias %s", addrPort, server.Host)
		cl.Server = server.String()
	}

	if nw != "" {
		dcName := daemonID.ContainerName()
		clog.Debugf(ctx, "Connecting network %s to container %s", nw, dcName)
		if err = cli.NetworkConnect(ctx, nw, dcName, nil); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				clog.Debugf(ctx, "failed to connect network %s to container %s: %v", nw, dcName, err)
			}
		}
	}
	return nil
}

// LaunchDaemon ensures that the image returned by ClientImage exists by calling PullImage. It then uses the
// options DaemonOptions and DaemonArgs to start the image, and finally connectDaemon to connect to it. A
// successful start yields a cache.Info entry in the cache.
func LaunchDaemon(ctx context.Context, daemonID *daemon.Identifier) (info *daemon.Info, conn *grpc.ClientConn, err error) {
	image := ClientImage(ctx)
	if err = PullImage(progress.WithEventId(ctx, daemonID.Name), image); err != nil {
		return nil, nil, errcat.NoDaemonLogs.New(err)
	}
	fp, err := ioutil.FreePortsTCP(1)
	if err != nil {
		return nil, nil, errcat.NoDaemonLogs.New(err)
	}
	daemonAddr := fp[0]
	opts, err := DaemonOptions(ctx, daemonID, daemonAddr.Port())
	if err != nil {
		return nil, nil, errcat.NoDaemonLogs.New(err)
	}
	args := DaemonArgs(ctx, daemonID)

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
		info, err = tryLaunch(ctx, daemonID, daemonAddr.Port(), allArgs)
		if err != nil {
			if !strings.Contains(err.Error(), "already in use by container") {
				return nil, nil, errcat.NoDaemonLogs.New(err)
			}
			// This may happen if the daemon has died (and hence, we never discovered it), but
			// the container still hasn't died. Let's sleep for a short while and retry.
			if i < 6 {
				time.Sleep(time.Duration(i) * 500 * time.Millisecond)
				continue
			}
			if stopAttempted {
				return nil, nil, err
			}
			// The Container is still alive. Try and stop it.
			_ = StopContainer(ctx, daemonID.ContainerName())
			stopAttempted = true
			i = 1
			continue
		}
		break
	}

	if err = enableK8SAuthenticator(ctx, daemonID); err != nil {
		return nil, nil, err
	}
	conn, err = ConnectDaemon(ctx, info)
	if err != nil {
		return nil, nil, err
	}
	return info, conn, nil
}

// containerPort returns the port that the container uses internally to expose the given
// addrPort on the host. Zero is returned when the addrPort is not found among
// the container's port bindings.
// The additional bool is true if the host address is IPv6.
func containerPort(addrPort netip.AddrPort, ns *container.NetworkSettings) (port uint16, isIPv6 bool) {
	// If the port mapping exists where the source address is the host's address, then use the destination port.
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

	// If the address on the host belongs to a network, then trust the current port.
	addr := addrPort.Addr()
	for _, nw := range ns.Networks {
		if ic := nw.IPAMConfig; ic != nil {
			if addr.Is4() && ic.IPv4Address != "" {
				na, err := netip.ParseAddr(ic.IPv4Address)
				if err == nil && addr == na {
					return addrPort.Port(), false
				}
			}
			if addr.Is6() && ic.IPv6Address != "" {
				na, err := netip.ParseAddr(ic.IPv6Address)
				if err == nil && addr == na {
					return addrPort.Port(), false
				}
			}
		}
	}
	return 0, false
}

// runningContainers returns the inspect data for all containers with status=running.
func runningContainers(ctx context.Context, cli dockerClient.APIClient) []*container.InspectResponse {
	cl, err := cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.KeyValuePair{Key: "status", Value: "running"}),
	})
	if err != nil {
		clog.Errorf(ctx, "failed to list containers: %v", err)
		return nil
	}
	cjs := make([]*container.InspectResponse, 0, len(cl))
	for _, cn := range cl {
		cj, err := cli.ContainerInspect(ctx, cn.ID)
		if err != nil {
			clog.Errorf(ctx, "container inspect on %v failed: %v", cn.Names, err)
		} else {
			cjs = append(cjs, &cj)
		}
	}
	return cjs
}

func endpointAddr(cn *network.EndpointResource, isIPv6 bool) (addr netip.Addr, _ error) {
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
	return addr, err
}

func localAddr(ctx context.Context, cli dockerClient.APIClient, cnID, nwID string, isIPv6 bool) (addr netip.Addr, err error) {
	nw, err := cli.NetworkInspect(ctx, nwID, network.InspectOptions{})
	if err != nil {
		return addr, err
	}
	if cn, ok := nw.Containers[cnID]; ok {
		// These aren't IP-addresses at all. They are prefixes!
		return endpointAddr(&cn, isIPv6)
	}
	return addr, errors.New("no such container")
}

func findNetworkSettingsForHostPort(cns []*container.InspectResponse, hostAddrPort netip.AddrPort) (*container.InspectResponse, uint16, bool) {
	for _, cn := range cns {
		if ns := cn.NetworkSettings; ns != nil {
			if port, isIPv6 := containerPort(hostAddrPort, ns); port != 0 {
				return cn, port, isIPv6
			}
		}
	}
	return nil, 0, false
}

type containerFilter func(cn *container.InspectResponse) bool

//nolint:gochecknoglobals // constant
var knownFilters = map[string]containerFilter{
	"minikube": func(cn *container.InspectResponse) bool {
		return cn.Config != nil && cn.Config.Labels["name.minikube.sigs.k8s.io"] != ""
	},
	"k3s": func(cn *container.InspectResponse) bool {
		return cn.Config != nil && strings.Contains(cn.Config.Image, "/k3s:")
	},
	"kind": func(cn *container.InspectResponse) bool {
		return cn.Config != nil && cn.Config.Labels["io.x-k8s.kind.role"] == "control-plane"
	},
}

func detectControlPlane(ctx context.Context, cli dockerClient.APIClient, cns []*container.InspectResponse, hostAddr netip.AddrPort) (ap netip.AddrPort, nn string) {
	ncn, port, isIPv6 := findNetworkSettingsForHostPort(cns, hostAddr)
	if ncn == nil {
		clog.Debugf(ctx, "no network settings found that maps host address %s", hostAddr)
		return ap, nn
	}

	type candidate struct {
		container   *container.InspectResponse
		networkName string
		localAddr   netip.AddrPort
	}

	ns := ncn.NetworkSettings
	candidates := make([]candidate, 0)
	for _, cn := range cns {
		for networkName, nw := range ns.Networks {
			addr, err := localAddr(ctx, cli, cn.ID, nw.NetworkID, isIPv6)
			if err == nil {
				candidates = append(candidates, candidate{
					container:   cn,
					networkName: networkName,
					localAddr:   netip.AddrPortFrom(addr, port),
				})
			}
		}
	}

	switch len(candidates) {
	case 0:
		break
	case 1:
		c := candidates[0]
		clog.Debugf(ctx, "found control-plane %s(%s) for host address %s on network %q", c.container.Name, c.localAddr, hostAddr, c.networkName)
		return c.localAddr, c.networkName
	default:
		// We have multiple candidates. Let's try and discriminate using the known filters.'
		for _, c := range candidates {
			for filterName, filter := range knownFilters {
				if filter(c.container) {
					clog.Debugf(ctx, "found control-plane %s(%s) for host address %s on network %q using filter %q",
						c.container.Name, c.localAddr, hostAddr, c.networkName, filterName)
					return c.localAddr, c.networkName
				}
			}
		}
	}
	clog.Debugf(ctx, "found no control-plane for host address %s, container %s, container port %d", hostAddr, ncn.Name, port)
	return ap, nn
}

func tryLaunch(ctx context.Context, daemonID *daemon.Identifier, port uint16, args []string) (*daemon.Info, error) {
	stdErr := bytes.Buffer{}
	stdOut := bytes.Buffer{}
	clog.Debug(ctx, shellquote.ShellString(Exe, args))
	cmd := proc.CommandContext(ctx, Exe, args...)
	cmd.Stderr = &stdErr
	cmd.Stdout = &stdOut
	err := cmd.Run()
	cid := strings.TrimSpace(stdOut.String())
	errStr := strings.TrimSpace(stdErr.String())
	if errStr != "" || err != nil {
		err = fmt.Errorf("launch of daemon container failed: %s%s: %w", cid, errStr, err)
		clog.Error(ctx, err)
		return nil, err
	}

	// The teleroute network plugin communicates with the daemon over the default bridge network
	cni, err := GetContainerInfo(ctx, cid, "bridge")
	if err != nil {
		progress.Error(ctx, err.Error())
		return nil, err
	}
	cr := daemon.MustGetRequest(ctx)
	clog.Debugf(ctx, "Creating daemon info file %s (runs in container)", daemonID.Name)
	var ip netip.Addr
	if cni.IPv4.IsValid() {
		ip = cni.IPv4
	} else {
		ip = cni.IPv6
	}
	info := &daemon.Info{
		ContainerID:  cid,
		ContainerPID: cni.Pid,
		ContainerIP:  ip,
		DaemonPort:   port,
		Name:         daemonID.Name,
		KubeContext:  daemonID.KubeContext,
		Namespace:    daemonID.Namespace,
		ExposedPorts: cr.ExposedPorts,
		Hostname:     cr.Hostname,
	}
	return info, daemon.NewUserInfoLoader(ctx).SaveInfo(info, daemonID.InfoFileName())
}

func WaitForExit(ctx context.Context, cli *dockerClient.Client, id string, maxTime time.Duration) error {
	const exitPollInterval = 200 * time.Millisecond
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(exitPollInterval):
	}
	stillRunning := fmt.Errorf("container %s is still running", id)
	opts := container.ListOptions{Filters: filters.NewArgs(filters.Arg("id", id))}
	return backoff.Retry(func() error {
		lst, err := cli.ContainerList(ctx, opts)
		if err != nil {
			err = backoff.Permanent(err)
		} else if len(lst) > 0 {
			err = stillRunning
		}
		return err
	}, backoff.WithContext(backoff.NewExponentialBackOff(backoff.WithInitialInterval(exitPollInterval), backoff.WithMaxElapsedTime(maxTime)), ctx))
}

func appendOSSpecificContainerOpts(ctx context.Context, opts []string) ([]string, error) {
	if proc.RunningInWSL() {
		// Using host.docker.internal:host-gateway won't work for the kubeauth process, because Windows Docker Desktop
		// will assign the IP of the Windows host, not the host from where this process was started (the Linux host).
		// We'll reach that using the gateway of the default host.
		r, err := routing.DefaultRoute(ctx)
		if err != nil {
			return opts, err
		}
		opts = append(opts, "-e", fmt.Sprintf("TELEPRESENCE_KUBEAUTH_HOST=%s", r.LocalIP))
	}
	return opts, nil
}

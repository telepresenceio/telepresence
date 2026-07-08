//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"

	"github.com/google/nftables"
	"github.com/vishvananda/netns"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentnft"
	"github.com/telepresenceio/telepresence/v2/pkg/cri"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	tpnetns "github.com/telepresenceio/telepresence/v2/pkg/netns"
	"github.com/telepresenceio/telepresence/v2/pkg/nftutil"
	"github.com/telepresenceio/telepresence/v2/pkg/procfs"
	"github.com/telepresenceio/telepresence/v2/pkg/sigctx"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

const (
	// nodeAgentPacketMark is the firewall mark the node-agent sets on its own
	// sockets (SO_MARK, wired in a later change) and matches in the mesh-bypass
	// rule for user-namespaced targets, where a socket-owner match cannot be used.
	nodeAgentPacketMark = 0x2374
)

// nodeConfig is the sidecar Config, augmented with the host PID that each configured
// container resolves to on this node. It satisfies the full Config interface through the
// embedded *config and overrides AppEnviron to source the environment from procfs instead
// of the node-agent's own process.
type nodeConfig struct {
	*config
	pids map[string]int

	// targetPodIP is the IP of the pod the node-agent targets. It is the PodIP
	// of the netfilter ruleset programmed into the target's network namespace,
	// and the address the forwarders dial for non-intercepted traffic.
	targetPodIP netip.Addr
}

// AppEnviron reads the environment of the container's target process from procfs and applies
// the same transformation the sidecar applies to its own process environment.
func (c *nodeConfig) AppEnviron(_ context.Context, cn *agentconfig.Container) (map[string]string, error) {
	pid, ok := c.pids[cn.Name]
	if !ok {
		return nil, fmt.Errorf("no resolved PID for container %q", cn.Name)
	}
	env, err := procfs.Environ(pid)
	if err != nil {
		return nil, err
	}
	return appEnvironment(dos.MapEnv(env).Environ(), cn), nil
}

// nsListenerFactory creates listen sockets inside the network namespace at
// nsPath, so a node-agent forwarder binds its port in the target pod's
// namespace rather than the node-agent's own.
type nsListenerFactory struct{ nsPath string }

func (f nsListenerFactory) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	return tpnetns.Listen(ctx, f.nsPath, network, address)
}

func (f nsListenerFactory) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	return tpnetns.ListenPacket(ctx, f.nsPath, network, address)
}

// nsDialer dials from inside the network namespace at nsPath, so a node-agent
// forwarder's non-intercepted (pass-through) connection originates in the target
// pod's namespace and traverses that namespace's proxy-port DNAT.
type nsDialer struct{ nsPath string }

func (d nsDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return tpnetns.Dial(ctx, d.nsPath, network, address)
}

// DialerFactory returns a dialer that dials inside the target pod's network
// namespace, mirroring ListenerFactory on the outbound side. If no PID has been
// resolved (which should never happen for a running node-agent), nil is returned
// and the caller falls back to dialing in the node-agent's own namespace.
func (c *nodeConfig) DialerFactory() forwarder.Dialer {
	pid, err := targetPID(c.pids)
	if err != nil {
		return nil
	}
	return nsDialer{nsPath: tpnetns.PathForPID(pid)}
}

// AppPodIP returns the target pod's IP: the node-agent's application runs in a
// separate pod from the agent, and pass-through traffic must be dialed there.
func (c *nodeConfig) AppPodIP() netip.Addr {
	return c.targetPodIP
}

// ListenerFactory returns a factory that binds a container's forwarders inside the
// target pod's network namespace. All of a pod's containers share one network
// namespace, so a single factory serves them all. If no PID has been resolved for
// the target pod (which should never happen for a running node-agent), nil is
// returned and the caller falls back to listening in the node-agent's own
// namespace.
func (c *nodeConfig) ListenerFactory() forwarder.ListenerFactory {
	pid, err := targetPID(c.pids)
	if err != nil {
		return nil
	}
	return nsListenerFactory{nsPath: tpnetns.PathForPID(pid)}
}

// NodeAgent reports true: this agent is a Job in the traffic-manager's
// namespace that enters the target pod's namespaces.
func (c *nodeConfig) NodeAgent() bool {
	return true
}

// parseContainerIDs decodes the JSON object mapping container name to CRI container ID.
func parseContainerIDs(raw string) (map[string]string, error) {
	var ids map[string]string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", agentconfig.EnvNodeAgentContainerIDs, err)
	}
	return ids, nil
}

// loadNodeConfig loads the sidecar AGENT_CONFIG and pod identity like the sidecar agent, then
// resolves each configured container's host PID via the CRI socket and exports its remote
// mounts from procfs. Unlike LoadConfig, it does not run the sidecar-only addAppMounts step.
func loadNodeConfig(ctx context.Context) (*nodeConfig, error) {
	base, err := loadBaseConfig(ctx)
	if err != nil {
		return nil, err
	}

	raw, ok := dos.LookupEnv(ctx, agentconfig.EnvNodeAgentContainerIDs)
	if !ok {
		return nil, fmt.Errorf("missing %s", agentconfig.EnvNodeAgentContainerIDs)
	}
	ids, err := parseContainerIDs(raw)
	if err != nil {
		return nil, err
	}

	socket, ok := dos.LookupEnv(ctx, agentconfig.EnvNodeAgentCRISocket)
	if !ok || socket == "" {
		if socket, err = cri.DetectSocket(); err != nil {
			return nil, err
		}
	}

	podIPStr, ok := dos.LookupEnv(ctx, agentconfig.EnvNodeAgentPodIP)
	if !ok || podIPStr == "" {
		return nil, fmt.Errorf("missing %s", agentconfig.EnvNodeAgentPodIP)
	}
	targetPodIP, err := netip.ParseAddr(podIPStr)
	if err != nil {
		return nil, fmt.Errorf("invalid %s %q: %w", agentconfig.EnvNodeAgentPodIP, podIPStr, err)
	}

	sc := base.AgentConfig()
	pids := make(map[string]int, len(sc.Containers))
	for _, cn := range sc.Containers {
		id, ok := ids[cn.Name]
		if !ok {
			return nil, fmt.Errorf("no container ID for container %q in %s", cn.Name, agentconfig.EnvNodeAgentContainerIDs)
		}
		pid, err := cri.ResolvePID(ctx, socket, id)
		if err != nil {
			return nil, fmt.Errorf("resolve PID for container %q: %w", cn.Name, err)
		}
		pids[cn.Name] = pid
	}

	for _, cn := range sc.Containers {
		if err := exportProcMounts(ctx, agentconfig.ExportsMountPoint, pids[cn.Name], cn, sc.MountPolicies); err != nil {
			return nil, err
		}
	}

	return &nodeConfig{config: base, pids: pids, targetPodIP: targetPodIP}, nil
}

// exportProcMounts populates exportsRoot/<base of cn.MountPoint> with symlinks into pid's
// mount namespace: one per remote mount path declared in cn.Mounts, plus any pod-injected
// /var/run/secrets subdirectory of the target that isn't already covered by cn.Mounts. The
// FTP client strips the ExportsMountPoint prefix from the reported MountPoint (a client-side
// contract that is out of scope here), so serving must stay rooted under exportsRoot rather
// than exposing /proc/<pid>/root directly; the symlinks resolve in the target's mount
// namespace when the node-agent's ftp/sftp server follows them.
func exportProcMounts(ctx context.Context, exportsRoot string, pid int, cn *agentconfig.Container, mps types.MountPolicies) error {
	clog.Infof(ctx, "Exporting procfs mounts for container %s", cn.Name)
	cnMountPoint := filepath.Join(exportsRoot, filepath.Base(cn.MountPoint))
	if err := dos.Mkdir(ctx, cnMountPoint, 0o700); err != nil {
		if !os.IsExist(err) {
			return err
		}
		if err := dos.RemoveAll(ctx, cnMountPoint); err != nil {
			return err
		}
		if err := dos.Mkdir(ctx, cnMountPoint, 0o700); err != nil {
			return err
		}
	}

	for path, policy := range cn.Mounts {
		switch policy {
		case types.MountPolicyRemote, types.MountPolicyRemoteReadOnly:
		default:
			continue
		}
		target := filepath.Join(cnMountPoint, path)
		if err := dos.MkdirAll(ctx, filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := dos.Symlink(ctx, procfs.RootPath(pid, path), target); err != nil {
			return err
		}
	}

	if err := mountVRS(ctx, mps, cn, cnMountPoint, procfs.RootPath(pid, "/")); err != nil {
		return err
	}

	// Verify that all mounts exists, so that the client doesn't attempt to mount nonexistent paths
	for path, policy := range cn.Mounts {
		mp := filepath.Join(cnMountPoint, path)
		if policy == types.MountPolicyRemote || policy == types.MountPolicyRemoteReadOnly {
			_, err := dos.Stat(ctx, mp)
			if err != nil {
				clog.Infof(ctx, "Failed to stat %q. It will not be exported: %v", mp, err)
				delete(cn.Mounts, path)
			}
		}
	}
	return nil
}

// targetPID returns a host PID that resolves into the target pod's network namespace. A
// pod's containers all share the same network namespace, so any resolved PID works; the
// smallest is picked to make the choice deterministic.
func targetPID(pids map[string]int) (int, error) {
	if len(pids) == 0 {
		return 0, errors.New("no resolved PID for the target pod")
	}
	pid := 0
	for _, p := range pids {
		if pid == 0 || p < pid {
			pid = p
		}
	}
	return pid, nil
}

// nodeAgentGID returns the primary group the node-agent's own sockets carry, mirroring
// agentinit's trafficAgentOwner: AGENT_GID when the sidecar config set one, otherwise the
// group the node-agent's own container runs as (see buildNodeAgentJob's RunAsGroup).
func nodeAgentGID() (uint32, error) {
	if gid, ok := os.LookupEnv(agentconfig.EnvAgentGID); ok && gid != "" {
		parsed, err := strconv.ParseUint(gid, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid %s %q: %w", agentconfig.EnvAgentGID, gid, err)
		}
		return uint32(parsed), nil
	}
	return uint32(agentconfig.DefaultAgentGID), nil
}

// buildNodeAgentNftConfig translates cfg into the agentnft ruleset config for the target
// pod, mirroring agentinit's buildNftConfig. It is reimplemented here rather than shared
// with agentinit because pkg/agentconfig is cross-platform and must not import the
// linux-only pkg/agentnft.
func buildNodeAgentNftConfig(cfg *nodeConfig, podIP netip.Addr, owner agentnft.OwnerMatch) agentnft.Config {
	sc := cfg.AgentConfig()
	var intercepts []agentnft.Intercept
	for _, cn := range sc.Containers {
		for _, ic := range agentconfig.PortUniqueIntercepts(cn) {
			nic := agentnft.Intercept{
				Protocol:      ic.Protocol,
				ContainerPort: ic.ContainerPort,
				AgentPort:     ic.AgentPort,
			}
			if ic.TargetPortNumeric {
				nic.ProxyPort = sc.ProxyPort(ic.AgentPort)
			}
			intercepts = append(intercepts, nic)
		}
	}
	return agentnft.Config{
		PodIP:           podIP,
		Loopback:        "lo",
		Owner:           owner,
		Intercepts:      intercepts,
		MeshDialSubnets: sc.MeshDialSubnets,
	}
}

// applyNodeAgentRules programs the target pod's packet-routing rules into its network
// namespace. The ruleset is the same one the sidecar's init container installs (see
// agentinit.Main); here it is applied to another pod's namespace over netlink via
// nftables.WithNetNSFd. Returns a teardown func that removes the table again.
func applyNodeAgentRules(ctx context.Context, cfg *nodeConfig) (func(context.Context) error, error) {
	pid, err := targetPID(cfg.pids)
	if err != nil {
		return nil, err
	}
	podIP := cfg.targetPodIP

	// A socket-owner match cannot be installed into a network namespace owned by a
	// non-init user namespace (the kernel rejects it with EINVAL), so such targets are
	// told apart by firewall mark instead.
	inUserNS, err := procfs.InUserNamespace(pid)
	if err != nil {
		return nil, fmt.Errorf("determine user namespace of pid %d: %w", pid, err)
	}
	var owner agentnft.OwnerMatch
	discriminator := "mark"
	if inUserNS {
		owner = agentnft.OwnerMatch{Mark: nodeAgentPacketMark}
	} else {
		discriminator = "skgid"
		gid, gidErr := nodeAgentGID()
		if gidErr != nil {
			return nil, gidErr
		}
		owner = agentnft.OwnerMatch{UseGID: true, ID: gid}
	}

	rs, err := agentnft.Build(buildNodeAgentNftConfig(cfg, podIP, owner))
	if err != nil {
		return nil, err
	}

	nsPath := tpnetns.PathForPID(pid)
	h, err := netns.GetFromPath(nsPath)
	if err != nil {
		return nil, fmt.Errorf("open network namespace %q: %w", nsPath, err)
	}
	defer h.Close()

	if err := nftutil.Apply(ctx, &rs.Ruleset, nftables.WithNetNSFd(int(h))); err != nil {
		return nil, fmt.Errorf("apply packet-routing rules to pid %d's network namespace: %w", pid, err)
	}
	clog.Infof(ctx, "Programmed packet-routing rules into pid %d's network namespace (family %d, discriminator %s)",
		pid, rs.Table.Family, discriminator)

	teardown := func(ctx context.Context) error {
		h2, err := netns.GetFromPath(nsPath)
		if err != nil {
			return fmt.Errorf("open network namespace %q: %w", nsPath, err)
		}
		defer h2.Close()
		return nftutil.Teardown(ctx, agentnft.TableName, rs.Table.Family, nftables.WithNetNSFd(int(h2)))
	}
	return teardown, nil
}

// NodeAgentMain is the entrypoint for the node-agent Job pod. It mirrors Main, but loads its
// config from procfs/CRI-resolved target identity instead of its own process environment and
// filesystem, and runs NodeAgentSidecar instead of Sidecar.
func NodeAgentMain(ctx context.Context, _ ...string) error {
	debug.SetTraceback("single")
	clog.Infof(ctx, "Traffic Agent (node) %s", version.Version)

	return sigctx.DoWithSignalHandler(ctx, func(ctx context.Context) error {
		cfg, err := loadNodeConfig(ctx)
		if err != nil {
			return fmt.Errorf("unable to load config: %w", err)
		}

		// Program the target pod's packet-routing rules before starting any
		// services. NodeAgentSidecar starts the forwarders that receive the
		// redirected traffic, binding their listen sockets in the target pod's
		// network namespace via the Config's ListenerFactory.
		teardown, err := applyNodeAgentRules(ctx, cfg)
		if err != nil {
			return fmt.Errorf("unable to program packet-routing rules: %w", err)
		}
		defer func() {
			if err := teardown(ctx); err != nil {
				clog.Error(ctx, err)
			}
		}()

		g := log.NewGroup(ctx)
		s, err := NewState(ctx, cfg)
		if err != nil {
			return err
		}
		info, err := StartServices(g, cfg, s)
		if err != nil {
			return err
		}

		certsReadyCh := make(chan struct{})
		s.TLSManager().StartWatchers(g, certsReadyCh)

		g.Go("node-agent", func(ctx context.Context) error {
			<-certsReadyCh
			return NodeAgentSidecar(g, s, info)
		})

		// Wait for exit
		return g.Wait()
	})
}

// NodeAgentSidecar registers each container's env and mount info and starts its port
// handlers, mirroring Sidecar. The forwarders bind their listen sockets in the target
// pod's network namespace rather than the node-agent's own, via the ListenerFactory
// that Config supplies (see nodeConfig.ListenerFactory).
func NodeAgentSidecar(g log.Group, s State, info *rpc.AgentInfo) error {
	ac := s.AgentConfig()
	for _, cn := range ac.Containers {
		ci := info.Containers[cn.Name]
		cs := s.NewContainerState(s, cn, ci.MountPoint, ci.Environment)
		s.AddContainerState(cn.Name, cs)
		for pp, ics := range MakeInterceptStates(cn) {
			cs.AddPortHandler(g, pp, ics)
		}
	}
	TalkToManagerLoop(g, s, info)
	return nil
}

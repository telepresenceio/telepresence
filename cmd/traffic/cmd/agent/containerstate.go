package agent

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent/fwd"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type containerState struct {
	State
	container  *agentconfig.Container
	mountPoint string
	env        map[string]string
}

func (c *containerState) AddPortHandler(g log.Group, pp types.PortAndProto, it agentconfig.InterceptTarget) {
	ph := c.newPortHandler(g, pp, it)
	g.Go(fmt.Sprintf("forward-%s-%s:%d", c.container.Name, it.Protocol(), it.ContainerPort()), func(ctx context.Context) error {
		return ph.Serve(tunnel.WithPool(ctx, tunnel.NewPool()), nil)
	})
	c.AddInterceptState(c.NewInterceptState(ph, it, c.container.Name))
}

func (c *containerState) newPortHandler(ctx context.Context, pp types.PortAndProto, ics []*agentconfig.Intercept) fwd.Interceptor {
	ic := ics[0] // They all have the same protocol container port, so the first one will do.
	var opts []forwarder.Option
	if lf := c.ListenerFactory(); lf != nil {
		opts = append(opts, forwarder.WithListener(lf))
	}
	if d := c.DialerFactory(); d != nil {
		opts = append(opts, forwarder.WithDialer(d))
	}
	if pp.Proto == types.ProtoTCP && c.container.Replace == agentconfig.ReplacePolicyIntercept {
		// Traffic addressed to the pod IP's app ports is always redirected to
		// the agent (nftables "gate 2", programmed by agent-init for a
		// sidecar and by the node-agent for its target -- both build the
		// same ruleset from agentnft.ConfigFor), so that a client dialing
		// through the tunnel intercept reaches the agent regardless of who's
		// asking. That means the agent's own pass-through dial to the real
		// app -- made here, once, when no intercept is active -- must land
		// somewhere the redirect doesn't reach:
		//   - numeric target port: the proxy port. InterceptorInactivePort
		//     returns that port, and the proxy-port DNAT rewrites pod-IP
		//     dials to it back to the container port, breaking the loop.
		//   - named target port: there is no proxy port (no nft rewrite
		//     targets a name), so InterceptorInactivePort returns the
		//     container port unchanged. Dialing the pod IP there would hit
		//     the same unconditional redirect and loop back into the agent.
		//     Dial loopback instead: "gate 3" deliberately exempts
		//     127.0.0.1/::1 so the agent's direct-to-app dial can reach it.
		//     This requires the app to listen on loopback or a wildcard
		//     address -- the same assumption the loopback gate already made.
		cp := c.AgentConfig().InterceptorInactivePort(ic.ContainerPort, pp.Proto)
		targetIP := c.AppPodIP()
		if cp == ic.ContainerPort {
			targetIP = loopbackFor(targetIP)
		}
		defaultTarget := netip.AddrPortFrom(targetIP, cp)
		return fwd.NewTCPInterceptor(ctx, pp, tunnel.AgentToClient, c.TLSManager(), defaultTarget, opts...)
	}
	// The agent will intercept all traffic intended for this container.
	return fwd.NewInterceptor(ctx, pp, tunnel.AgentToClient, netip.AddrPort{}, opts...)
}

// loopbackFor returns the loopback address of the same address family as ip.
func loopbackFor(ip netip.Addr) netip.Addr {
	if ip.Is6() {
		return netip.IPv6Loopback()
	}
	return netip.AddrFrom4([4]byte{127, 0, 0, 1})
}

func (c *containerState) GlobalState() State {
	return c.State
}

func (c *containerState) Container() *agentconfig.Container {
	return c.container
}

func (c *containerState) MountPoint() string {
	return c.mountPoint
}

func (c *containerState) Mounts() types.MountPolicies {
	return c.container.Mounts
}

func (c *containerState) Env() map[string]string {
	return c.env
}

func (c *containerState) Name() string {
	return c.container.Name
}

func (c *containerState) ReplaceContainer() bool {
	return c.container.Replace == agentconfig.ReplacePolicyContainer
}

// HandleContainer on the containerState takes care of intercepts that just replaces a container and do not declare
// any ports. Without port declarations, there will be no Intercept entries for an fwdState to handle.
func (c *containerState) HandleContainer(ctx context.Context, iis []*manager.InterceptInfo) (rs []*manager.ReviewInterceptRequest) {
	for _, ii := range iis {
		if ii.Disposition == manager.InterceptDispositionType_WAITING {
			spec := ii.Spec
			if c.ReplaceContainer() && c.Name() == spec.ContainerName && spec.ContainerPort == 0 {
				clog.Debugf(ctx, "container %s handling replace %s", c.Name(), spec.Name)
				rs = append(rs, &manager.ReviewInterceptRequest{
					Id:          ii.Id,
					Disposition: manager.InterceptDispositionType_ACTIVE,
					PodIp:       c.PodIP().String(),
					SftpPort:    int32(c.SftpPort()),
					FtpPort:     int32(c.FtpPort()),
					MountPoint:  c.MountPoint(),
					Mounts:      c.Mounts().ToRPC(),
					Environment: c.Env(),
				})
			}
		}
	}
	return rs
}

// NewContainerState creates a ContainerState that provides the environment variables and the mount point for a container.
func (s *state) NewContainerState(gs State, cn *agentconfig.Container, mountPoint string, env map[string]string) ContainerState {
	return &containerState{
		State:      gs,
		container:  cn,
		mountPoint: mountPoint,
		env:        env,
	}
}

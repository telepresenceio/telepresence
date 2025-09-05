package agent

import (
	"context"
	"fmt"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type containerState struct {
	State
	container  *agentconfig.Container
	mountPoint string
	env        map[string]string
}

func (c *containerState) AddPortHandler(ctx context.Context, pp types.PortAndProto, ics []*agentconfig.Intercept) forwarder.Interceptor {
	fwd, cp := c.newPortHandler(pp, ics)
	dgroup.ParentGroup(ctx).Go(fmt.Sprintf("forward-%s", iputil.JoinHostPort(c.container.Name, cp)), func(ctx context.Context) error {
		return fwd.Serve(tunnel.WithPool(ctx, tunnel.NewPool()), nil)
	})
	c.AddInterceptState(c.NewInterceptState(fwd, NewInterceptTarget(ics), c.container.Name))
	return fwd
}

func (c *containerState) newPortHandler(pp types.PortAndProto, ics []*agentconfig.Intercept) (forwarder.Interceptor, uint16) {
	ic := ics[0] // They all have the same protocol container port, so the first one will do.
	var fwd forwarder.Interceptor
	var cp uint16
	if c.container.Replace == agentconfig.ReplacePolicyIntercept {
		var tag tunnel.Tag
		if ic.TargetPortNumeric {
			// We must differentiate between connections originating from the agent's forwarder to the container
			// port and those from other sources. The former should not be routed back, while the latter should
			// always be routed to the agent. We do this by using a proxy port that will be recognized by the
			// iptables filtering in our init-container.
			tag = tunnel.AgentToProxied
			cp = c.AgentConfig().ProxyPort(ic)
		} else {
			tag = tunnel.AgentToClient
			cp = ic.ContainerPort
		}
		// Redirect non-intercepted traffic to the pod so that injected sidecars that hijack the ports for
		// incoming connections will continue to work.
		targetHost := c.PodIP()
		fwd = forwarder.NewInterceptor(pp, tag, targetHost, cp)
	} else {
		// The agent will intercept all traffic intended for this container.
		fwd = forwarder.NewInterceptor(pp, tunnel.AgentToClient, "", 0)
		cp = ic.ContainerPort
	}
	return fwd, cp
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

// HandleIntercepts on the containerState takes care of intercepts that just replaces a container and do not declare
// any ports. Without port declarations, there will be no Intercept entries for an fwdState to handle.
func (c *containerState) HandleIntercepts(ctx context.Context, iis []*manager.InterceptInfo) (rs []*manager.ReviewInterceptRequest) {
	for _, ii := range iis {
		if ii.Disposition == manager.InterceptDispositionType_WAITING {
			spec := ii.Spec
			if c.ReplaceContainer() && c.Name() == spec.ContainerName && spec.ContainerPort == 0 {
				dlog.Debugf(ctx, "container %s handling replace %s", c.Name(), spec.Name)
				rs = append(rs, &manager.ReviewInterceptRequest{
					Id:          ii.Id,
					Disposition: manager.InterceptDispositionType_ACTIVE,
					PodIp:       c.PodIP(),
					SftpPort:    int32(c.SftpPort()),
					FtpPort:     int32(c.FtpPort()),
					MountPoint:  c.MountPoint(),
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

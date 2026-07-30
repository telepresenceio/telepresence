package nodeagent

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// NodeAgentClientDefault proves the cluster-served client default for
// node-agent mode (client.nodeAgent.enabled=true): a plain intercept (no
// --node-agent flag) is served by a node-agent, and an explicit
// --node-agent=false overrides the default back to sidecar injection.
type NodeAgentClientDefault struct {
	rt.Suite
}

func init() {
	rt.Register(&NodeAgentClientDefault{}, rt.InArea("nodeagent"), rt.NeedsManager(managers.NodeAgentClientDefault()))
}

// Test_PlainInterceptUsesNodeAgent proves a flagless intercept is served by
// a node-agent Job: the pod stays untouched and a Job appears.
func (s *NodeAgentClientDefault) Test_PlainInterceptUsesNodeAgent() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := freshWorkload(t, ctx, s.R(), s.AppNamespace(), workloads.Echo("na-clientdefault-plain"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	mustDetach := true
	defer func() {
		if mustDetach {
			a.Detach(t)
		}
	}()

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	waitJobCount(&s.Suite, ctx, wl, 1)
	s.False(hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name),
		"a flagless intercept should use the cluster-served node-agent default, not a sidecar")

	a.Detach(t)
	mustDetach = false
	waitJobCount(&s.Suite, ctx, wl, 0)
}

// Test_FlagOverridesClusterDefault proves --node-agent=false overrides the
// cluster default: the intercept falls back to sidecar injection, so no
// node-agent Job is created and a traffic-agent container appears instead.
func (s *NodeAgentClientDefault) Test_FlagOverridesClusterDefault() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := freshWorkload(t, ctx, s.R(), s.AppNamespace(), workloads.Echo("na-clientdefault-override"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFalseFlag())
	defer a.Detach(t)

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	s.Empty(nodeAgentJobNames(ctx, s.R(), wl),
		"no node-agent Job should be created when --node-agent=false overrides the cluster default")
	s.True(hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name),
		"a traffic-agent sidecar should be injected when --node-agent=false overrides the cluster default")
}

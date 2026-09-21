package nodeagent

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// NodeAgentAuthEnforcing proves a node-agent attach authenticates to the
// manager under security.authentication.mode=enforcing: the granted default
// identity intercepts a workload in node-agent mode and traffic reaches the
// local handler.
type NodeAgentAuthEnforcing struct {
	rt.Suite
}

func init() {
	rt.Register(&NodeAgentAuthEnforcing{}, rt.InArea("nodeagent"), rt.NeedsManager(managers.NodeAgentAuthEnforcing()))
}

// Test_Intercept attaches a node-agent intercept against an enforcing-mode
// manager: traffic routes local, a Job appears, and detach reaps it.
func (s *NodeAgentAuthEnforcing) Test_Intercept() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := freshWorkload(t, ctx, s.R(), s.AppNamespace(), workloads.Echo("na-auth-enforcing-intercept"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFlag())
	mustDetach := true
	defer func() {
		if mustDetach {
			a.Detach(t)
		}
	}()

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	waitJobCount(&s.Suite, ctx, wl, 1)
	s.False(hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name),
		"a node-agent intercept must not inject a traffic-agent sidecar")

	a.Detach(t)
	mustDetach = false
	waitJobCount(&s.Suite, ctx, wl, 0)
}

package nodeagent

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// injectorDisabledSubstring is the error the manager returns for a sidecar
// (non-node-agent) intercept attempt when agentInjector.enabled is false
// (cmd/traffic/cmd/manager/state/intercept.go).
const injectorDisabledSubstring = "agent-injector is disabled"

// NodeAgentNoInjector proves node-agent mode is independent of the
// agent-injector webhook: intercepts still work with the webhook disabled,
// but falling back to a sidecar via --node-agent=false is rejected.
type NodeAgentNoInjector struct {
	rt.Suite
}

func init() {
	rt.Register(&NodeAgentNoInjector{}, rt.InArea("nodeagent"), rt.NeedsManager(managers.NodeAgentNoInjector()))
}

// Test_Intercept proves a node-agent intercept works end-to-end even though
// the agent-injector webhook is disabled cluster-wide.
func (s *NodeAgentNoInjector) Test_Intercept() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("na-noinjector-intercept"))
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

	a.Detach(t)
	mustDetach = false
	waitJobCount(&s.Suite, ctx, wl, 0)
}

// Test_SidecarFlagRejected proves an explicit --node-agent=false is
// rejected with the injector-disabled error: the sidecar path needs the
// disabled webhook.
func (s *NodeAgentNoInjector) Test_SidecarFlagRejected() {
	ctx := s.Ctx()
	s.Connect()
	wl := s.Workload(workloads.Echo("na-noinjector-flag"))
	ls := s.LocalEcho()

	args := []string{"intercept", wl.Name, "--namespace", wl.Namespace, "--format", "json"}
	for _, o := range []cli.InterceptOpt{rt.ToLocal(ls, "http"), cli.MountFalse(), nodeAgentFalseFlag()} {
		args = append(args, o()...)
	}
	stdout, stderr, err := s.CLI().Run(ctx, args...)
	s.Error(err, "a --node-agent=false intercept should be rejected while the agent-injector is disabled")
	s.Contains(stdout+stderr, injectorDisabledSubstring)
}

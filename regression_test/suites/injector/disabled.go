package injector

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// injectorDisabledErrorSubstring is the error the manager returns for an
// intercept attempt when agentInjector.enabled is false and no agent was
// added manually (cmd/traffic/cmd/manager/state/intercept.go).
const injectorDisabledErrorSubstring = "agent-injector is disabled"

// Disabled proves that agentInjector.enabled=false (managers.InjectorDisabled)
// blocks every intercept with a clear error while leaving unrelated CLI
// verbs (version, list) working.
type Disabled struct {
	rt.Suite
}

func init() {
	rt.Register(&Disabled{}, rt.InArea("injector"), rt.NeedsManager(managers.InjectorDisabled()))
}

func (s *Disabled) Test_InterceptFailsVersionAndListStillWork() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := switchManagerSpec(t, ctx, managers.InjectorDisabled(), ns)

	wl := rt.Get(t, rt.WorkloadFixture(ns, workloads.Echo("injector-disabled")))

	args := []string{"intercept", wl.Name, "--namespace", wl.Namespace, "--mount", "false"}
	_, stderr, err := s.CLI().Run(ctx, args...)
	s.Error(err, "intercept should fail while the agent-injector is disabled")
	s.Contains(stderr, injectorDisabledErrorSubstring)
	s.False(hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name), "no agent should have been injected")

	if _, _, err := s.CLI().Run(ctx, "version", "--format", "json"); err != nil {
		t.Fatalf("version should still work with the injector disabled: %v", err)
	}

	entries := conn.List(t)
	s.True(listContains(entries, wl.Name, wl.Namespace), "list should still show %s.%s", wl.Name, wl.Namespace)
}

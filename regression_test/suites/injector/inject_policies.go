package injector

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// injectPolicies are the agentInjector.injectPolicy enum values
// (charts/telepresence-oss/values.schema.yaml).
var injectPolicies = []string{"OnDemand", "WhenEnabled"} //nolint:gochecknoglobals // catalog-like constant list

// InjectPolicies proves the two injectPolicy values' documented semantics
// (integration_test/inject_policy_test.go): OnDemand injects a plain
// (unannotated) workload only once it is intercepted; WhenEnabled never
// injects a plain workload at all, only one carrying the enabled
// annotation. An annotated workload is always injected in advance,
// regardless of policy.
type InjectPolicies struct {
	rt.Suite
}

func init() {
	rt.Register(&InjectPolicies{}, rt.InArea("injector"))
}

// Test_Policies switches the shared release to each policy in turn (via
// switchManagerSpec, so a later area re-provisions whatever it needs) and
// checks both a plain and an annotated fresh workload against it.
func (s *InjectPolicies) Test_Policies() {
	for _, p := range injectPolicies {
		s.Run(p, func() { s.runPolicy(p) })
	}
}

func (s *InjectPolicies) runPolicy(policy string) {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := switchManagerSpec(t, ctx, managers.InjectPolicy(policy), ns)
	slug := strings.ToLower(policy)

	plain := workloads.Echo("inject-policy-" + slug + "-plain")
	plainWl := freshWorkload(t, ctx, s.R(), ns, plain)
	s.False(hasAgentContainer(ctx, s.R(), plainWl.Namespace, plainWl.Name),
		"policy %s: unannotated workload should have no agent before any intercept", policy)

	switch policy {
	case "OnDemand":
		ls := s.LocalEcho()
		a := conn.Intercept(t, plainWl, rt.ToLocal(ls, "http"), cli.MountFalse())
		defer a.Detach(t)
		s.Eventually(func() bool {
			return hasAgentContainer(ctx, s.R(), plainWl.Namespace, plainWl.Name)
		}, agentPollTimeout, agentPollInterval, "policy OnDemand: intercept should inject the agent on demand")
	case "WhenEnabled":
		args := []string{"intercept", plainWl.Name, "--namespace", plainWl.Namespace, "--mount", "false"}
		_, stderr, err := s.CLI().Run(ctx, args...)
		s.Error(err, "policy WhenEnabled: intercepting an unannotated workload should fail")
		s.NotEmpty(stderr, "policy WhenEnabled: a failed intercept should report why")
		s.False(hasAgentContainer(ctx, s.R(), plainWl.Namespace, plainWl.Name),
			"policy WhenEnabled: unannotated workload should still have no agent")
	default:
		t.Fatalf("unhandled inject policy %q", policy)
	}

	enabled := workloads.Echo("inject-policy-" + slug + "-enabled")
	enabled.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	enabledWl := freshWorkload(t, ctx, s.R(), ns, enabled)
	s.Eventually(func() bool {
		return hasAgentContainer(ctx, s.R(), enabledWl.Namespace, enabledWl.Name)
	}, agentPollTimeout, agentPollInterval, "policy %s: annotated workload should be injected in advance", policy)
}

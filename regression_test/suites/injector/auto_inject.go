package injector

import (
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AutoInject proves that a workload annotated
// telepresence.io/inject-traffic-agent: enabled gets a traffic-agent
// sidecar on rollout alone, without any intercept, and loses it again once
// the annotation is removed and the workload is recreated.
type AutoInject struct {
	rt.Suite
}

func init() {
	rt.Register(&AutoInject{}, rt.InArea("injector"), rt.NeedsManager(managers.Default))
}

// Test_AnnotationDrivesInjection rolls out an annotated workload, waits for
// the traffic-agent to appear with no intercept involved, then removes the
// annotation. That changes workloads.Template's fixture hash, so the next
// Get recreates the workload (fixture_workload.go's provisionWorkload
// deletes and reapplies on a manifest change rather than patching in place,
// since the manager preserves a workload's existing agent config across
// in-place edits) and the agent should be gone once it settles.
func (s *AutoInject) Test_AnnotationDrivesInjection() {
	t := s.T()
	ctx := s.Ctx()
	s.Manager()
	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	ns := rt.PrivateNamespace(env, "auto-inject")
	// ns is brand new: the already-running manager only manages it once its
	// pod restarts and re-lists namespaces.
	s.Require().NoError(rt.RestartManager(env))

	tpl := workloads.Echo("auto-inject")
	tpl.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	wl := rt.Get(t, rt.WorkloadFixture(ns, tpl))

	s.Eventually(func() bool {
		return hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name)
	}, agentPollTimeout, agentPollInterval, "traffic-agent should be present after rollout with no intercept")

	tpl.Annotations = nil
	wl = rt.Get(t, rt.WorkloadFixture(ns, tpl))

	s.Eventually(func() bool {
		return !hasAgentContainer(ctx, s.R(), wl.Namespace, wl.Name)
	}, agentPollTimeout, agentPollInterval, "traffic-agent should be gone after removing the annotation and recreating")
}

package nodeagent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// agentContainerName is the traffic-agent's container name inside an
// injected pod (agentconfig.ContainerName, "traffic-agent").
const agentContainerName = "traffic-agent"

// jobPollTimeout/jobPollInterval bound every poll for a node-agent Job to
// appear in, or be reaped from, the manager namespace: Job scheduling on a
// node takes seconds, so these are generous.
const (
	jobPollTimeout  = 90 * time.Second
	jobPollInterval = 2 * time.Second
)

// nodeAgentFlag builds --node-agent, requesting a node-hosted traffic-agent
// Job instead of an injected sidecar for the attach.
func nodeAgentFlag() cli.InterceptOpt {
	return func() []string { return []string{"--node-agent"} }
}

// nodeAgentFalseFlag builds --node-agent=false, overriding a cluster- or
// config-served node-agent default back to sidecar injection.
func nodeAgentFalseFlag() cli.InterceptOpt {
	return func() []string { return []string{"--node-agent=false"} }
}

// hasAgentContainer reports whether any pod matching app=name in ns
// currently carries a traffic-agent container. Duplicated from
// suites/injector/helpers.go: suite packages don't share private helpers,
// see that file's freshWorkload doc.
func hasAgentContainer(ctx context.Context, r *rt.Runtime, ns, name string) bool {
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+name, "-o",
		"jsonpath={.items[*].spec.containers[*].name}")
	if err != nil {
		return false
	}
	for _, f := range strings.Fields(out) {
		if f == agentContainerName {
			return true
		}
	}
	return false
}

// freshWorkload guarantees an agent-free starting state for a behavior-
// sensitive workload in the shared namespace: a previous run may have left
// the same-named workload with an injected agent, which dev-mode adoption
// would happily reuse. Deleting before provisioning forces a clean rollout;
// Mutate keeps the policy-contaminated result out of the memo for later
// suites. Duplicated from suites/injector/helpers.go (cross-package
// private).
func freshWorkload(t *testing.T, ctx context.Context, r *rt.Runtime, ns string, tpl workloads.Template) *rt.Workload {
	t.Helper()
	kindPath := strings.ToLower(tpl.Kind) + "/" + tpl.Name
	_, _ = r.Kubectl(ctx, ns, "delete", kindPath, "--ignore-not-found", "--wait")
	return rt.Mutate(t, rt.WorkloadFixture(ns, tpl))
}

// nodeAgentJobNames lists the node-agent Jobs backing wl in the manager
// namespace: one per targeted pod, selected by app=traffic-node-agent plus
// the workload's own agentName/workloadNamespace labels.
func nodeAgentJobNames(ctx context.Context, r *rt.Runtime, wl *rt.Workload) []string {
	selector := fmt.Sprintf(
		"app=traffic-node-agent,telepresence.io/agentName=%s,telepresence.io/workloadNamespace=%s",
		wl.Name, wl.Namespace)
	out, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "jobs", "-l", selector, "-o",
		"jsonpath={.items[*].metadata.name}")
	if err != nil {
		return nil
	}
	return strings.Fields(strings.TrimSpace(out))
}

// waitJobCount polls until wl has exactly n node-agent Jobs in the manager
// namespace, failing the test after jobPollTimeout.
func waitJobCount(s *rt.Suite, ctx context.Context, wl *rt.Workload, n int) {
	s.T().Helper()
	s.Require().Eventually(func() bool {
		return len(nodeAgentJobNames(ctx, s.R(), wl)) == n
	}, jobPollTimeout, jobPollInterval,
		"expected %d node-agent Job(s) for %s.%s", n, wl.Name, wl.Namespace)
}

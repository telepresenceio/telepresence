package injector

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// agentContainerName is the traffic-agent's container name inside an
// injected pod (agentconfig.ContainerName, "traffic-agent").
const agentContainerName = "traffic-agent"

// agentPollTimeout/agentPollInterval bound every poll for the traffic-agent
// container to appear on, or disappear from, a workload's pods.
const (
	agentPollTimeout  = 90 * time.Second
	agentPollInterval = 2 * time.Second
)

// hasAgentContainer reports whether any pod matching the app=name selector
// in ns currently carries a traffic-agent container.
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

// agentResources returns the traffic-agent container's resources.requests/
// limits for the first pod matching the app=name selector in ns.
func agentResources(ctx context.Context, r *rt.Runtime, ns, name string) (corev1.ResourceRequirements, error) {
	var rr corev1.ResourceRequirements
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+name, "-o",
		`jsonpath={range .items[0].spec.containers[?(@.name=='`+agentContainerName+`')]}{.resources}{end}`)
	if err != nil {
		return rr, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return rr, fmt.Errorf("no %s container found for app=%s in %s", agentContainerName, name, ns)
	}
	if err := yaml.Unmarshal([]byte(out), &rr); err != nil {
		return rr, err
	}
	return rr, nil
}

// listContains reports whether entries contains a workload named name in ns.
func listContains(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}

// switchManagerSpec frees (quits) whatever connection to ns is currently
// memoized while the release still runs its previous spec, switches the
// shared release to spec via rt.Mutate (so a later area re-provisions
// whatever spec it needs next, per docs/plans/regression-test-framework/
// m3-wave2-spec.md's ordering note), and reconnects to ns, returning the
// live *rt.Conn. The reconnect goes through rt.Reconnect rather than a
// second Mutate(ConnectionFixture(ns)): see fixture_connection.go's doc.
func switchManagerSpec(t *testing.T, ctx context.Context, spec managers.Spec, ns string) *rt.Conn {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)
	rt.Mutate(t, rt.ManagerFixture(spec))
	return rt.Reconnect(t, ctx, ns)
}

// freshWorkload guarantees an agent-free starting state for a behavior-
// sensitive workload in a shared namespace: a previous run may have left the
// same-named workload with an injected agent, which dev-mode adoption would
// happily reuse. Deleting before provisioning forces a clean rollout;
// Mutate keeps the policy-contaminated result out of the memo for later
// suites.
func freshWorkload(t *testing.T, ctx context.Context, r *rt.Runtime, ns string, tpl workloads.Template) *rt.Workload {
	t.Helper()
	kindPath := strings.ToLower(tpl.Kind) + "/" + tpl.Name
	_, _ = r.Kubectl(ctx, ns, "delete", kindPath, "--ignore-not-found", "--wait")
	return rt.Mutate(t, rt.WorkloadFixture(ns, tpl))
}

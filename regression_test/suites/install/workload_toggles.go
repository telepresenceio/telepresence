package install

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

// workloadListPollTimeout/workloadListPollInterval bound how long `list`
// takes to reflect a workload kind toggle: long enough for the manager's
// per-namespace watcher (cmd/traffic/cmd/manager/state/state.go's
// NewWatcher, constructed with managerutil.Env.EnabledWorkloadKinds) to have
// noticed the workload, whichever way the toggle should make it settle.
const (
	workloadListPollTimeout  = 30 * time.Second
	workloadListPollInterval = 2 * time.Second
)

// replicaSetsDisabledSpec disables workloads.replicaSets.enabled
// (charts/telepresence-oss/values.schema.yaml: "Enable/Disable the support
// for ReplicaSets"), which drops "ReplicaSet" from the chart-rendered
// ENABLED_WORKLOAD_KINDS env var and so from managerutil.Env.
// EnabledWorkloadKinds: the manager's per-namespace watcher (state.go's
// NewWatcher) never lists ReplicaSets, and PrepareIntercept
// (cmd/traffic/cmd/manager/state/intercept.go) skips that kind entirely when
// resolving an intercept target.
func replicaSetsDisabledSpec() managers.Spec {
	disabled := false
	return managers.Spec{
		Key:    "workloads-replicasets-disabled",
		Values: managers.Values{Workloads: managers.Workloads{ReplicaSets: managers.WorkloadKind{Enabled: &disabled}}},
	}
}

// statefulSetsDisabledSpec is replicaSetsDisabledSpec's counterpart for
// workloads.statefulSets.enabled.
func statefulSetsDisabledSpec() managers.Spec {
	disabled := false
	return managers.Spec{
		Key:    "workloads-statefulsets-disabled",
		Values: managers.Values{Workloads: managers.Workloads{StatefulSets: managers.WorkloadKind{Enabled: &disabled}}},
	}
}

// deploymentsDisabledSpec is replicaSetsDisabledSpec's counterpart for
// workloads.deployments.enabled.
func deploymentsDisabledSpec() managers.Spec {
	disabled := false
	return managers.Spec{
		Key:    "workloads-deployments-disabled",
		Values: managers.Values{Workloads: managers.Workloads{Deployments: managers.WorkloadKind{Enabled: &disabled}}},
	}
}

// switchManagerSpec frees the connection currently memoized for ns while the
// release still runs its previous spec, switches the shared release to spec
// via rt.Mutate (so a later area re-provisions whatever spec it needs next),
// and reconnects, returning the live *rt.Conn.
func switchManagerSpec(t *testing.T, ctx context.Context, spec managers.Spec, ns string) *rt.Conn {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)
	rt.Mutate(t, rt.ManagerFixture(spec))
	return rt.Reconnect(t, ctx, ns)
}

// workloadListed reports whether entries contains a workload named name in
// ns.
func workloadListed(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}

// workloadAttached reports whether the workload named name in ns currently
// carries an intercept.
func workloadAttached(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return len(e.InterceptInfo) > 0
		}
	}
	return false
}

// notFoundError is the stable core of the error CreateIntercept returns for
// a workload the manager cannot resolve: constructed in
// cmd/traffic/cmd/manager/state/intercept.go's PrepareIntercept as
// errcat.User.New(k8sErrors.NewNotFound(core.Resource("workload"),
// name+"."+ns)), a Kubernetes-style NotFound whose message is
// `<resource> %q not found`. It reaches the CLI unchanged: pkg/client/userd/
// trafficmgr/intercept.go's CanIntercept surfaces pi.Error verbatim, and
// AddIntercept calls CanIntercept directly, so the same text lands in the
// `intercept` command's stderr (wrapped there in a "connector.
// CreateIntercept: " prefix this assertion deliberately ignores).
func notFoundError(name, ns string) string {
	return fmt.Sprintf("workload %q not found", name+"."+ns)
}

// WorkloadToggles covers the chart's workloads.<kind>.enabled toggles
// (charts/telepresence-oss/values.schema.yaml): disabling a kind removes it
// from the manager's per-namespace watcher entirely
// (cmd/traffic/cmd/manager/state/state.go's NewWatcher, and the
// EnabledWorkloadKinds env var it's built from,
// cmd/traffic/cmd/manager/managerutil/envconfig.go), so a workload of that
// kind is never listed and never resolves as an intercept target, while
// other kinds keep working normally -- including a Deployment's own
// ReplicaSet becoming the attachable unit once Deployments are disabled.
// Each test switches the shared release to its own spec via switchManagerSpec
// (never assuming a previous test's spec) and reconnects fresh.
type WorkloadToggles struct {
	rt.Suite
}

func init() {
	rt.Register(&WorkloadToggles{}, rt.InArea("install"))
}

// Test_DisabledReplicaSetInvisible proves that a bare ReplicaSet workload
// never shows up in `list` while workloads.replicaSets.enabled=false, and
// that intercepting it by name fails with the same not-found error
// CreateIntercept returns for a workload that doesn't exist at all.
func (s *WorkloadToggles) Test_DisabledReplicaSetInvisible() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := switchManagerSpec(t, ctx, replicaSetsDisabledSpec(), ns)

	wl := s.Workload(workloads.EchoReplicaSet("toggle-rs-disabled"))

	s.Never(func() bool {
		return workloadListed(conn.List(t), wl.Name, wl.Namespace)
	}, workloadListPollTimeout, workloadListPollInterval,
		"%s should never appear in list while ReplicaSets are disabled", wl.Name)

	args := []string{"intercept", wl.Name, "--namespace", wl.Namespace, "--mount", "false"}
	_, stderr, err := s.CLI().Run(ctx, args...)
	s.Error(err, "intercept of a disabled-kind workload should fail")
	s.Contains(stderr, notFoundError(wl.Name, wl.Namespace))
}

// Test_DisabledStatefulSetInvisible is Test_DisabledReplicaSetInvisible's
// counterpart for workloads.statefulSets.enabled=false and a StatefulSet
// workload.
func (s *WorkloadToggles) Test_DisabledStatefulSetInvisible() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := switchManagerSpec(t, ctx, statefulSetsDisabledSpec(), ns)

	wl := s.Workload(workloads.EchoStatefulSet("toggle-ss-disabled"))

	s.Never(func() bool {
		return workloadListed(conn.List(t), wl.Name, wl.Namespace)
	}, workloadListPollTimeout, workloadListPollInterval,
		"%s should never appear in list while StatefulSets are disabled", wl.Name)

	args := []string{"intercept", wl.Name, "--namespace", wl.Namespace, "--mount", "false"}
	_, stderr, err := s.CLI().Run(ctx, args...)
	s.Error(err, "intercept of a disabled-kind workload should fail")
	s.Contains(stderr, notFoundError(wl.Name, wl.Namespace))
}

// Test_DeploymentInterceptsWithReplicaSetsDisabled proves that
// workloads.replicaSets.enabled=false leaves Deployment support untouched: a
// Deployment still lists as attachable and a plain intercept succeeds,
// reporting its kind through the CLI's "Using <kind> <name>" line
// (pkg/client/cli/intercept/state.go's create()) and showing up in `list`
// as intercepted.
func (s *WorkloadToggles) Test_DeploymentInterceptsWithReplicaSetsDisabled() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := switchManagerSpec(t, ctx, replicaSetsDisabledSpec(), ns)

	wl := s.Workload(workloads.Echo("toggle-rs-disabled-deploy"))

	s.Eventually(func() bool {
		return workloadListed(conn.List(t), wl.Name, wl.Namespace)
	}, workloadListPollTimeout, workloadListPollInterval,
		"Deployment %s should still show in list with ReplicaSets disabled", wl.Name)

	ls := s.LocalEcho()
	args := []string{
		"intercept", wl.Name, "--namespace", wl.Namespace,
		"--port", fmt.Sprintf("%d:http", ls.Port()), "--mount", "false",
	}
	stdout, stderr, err := s.CLI().Run(ctx, args...)
	s.Require().NoError(err, "intercept: %s", stderr)
	s.Contains(stdout, fmt.Sprintf("Using %s %s", "Deployment", wl.Name))

	s.True(workloadAttached(conn.List(t), wl.Name, wl.Namespace),
		"list should show %s as intercepted", wl.Name)

	_, stderr, err = s.CLI().Run(ctx, "detach", wl.Name, "-n", wl.Namespace)
	s.Require().NoError(err, "detach: %s", stderr)
}

// Test_ReplicaSetInterceptsWithDeploymentsDisabled proves that once
// workloads.deployments.enabled=false, a Deployment's own ReplicaSet becomes
// the attachable unit: it eventually shows up in `list` by its own
// (Deployment-generated) name, a plain intercept against that name succeeds
// and reports "Using ReplicaSet <name>", and it shows up in `list` as
// intercepted.
func (s *WorkloadToggles) Test_ReplicaSetInterceptsWithDeploymentsDisabled() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	wl := s.Workload(workloads.Echo("toggle-deploy-disabled"))
	rsOut, err := s.R().Kubectl(ctx, ns, "get", "replicasets", "-l", "app="+wl.Name,
		"-o", "jsonpath={.items[*].metadata.name}")
	s.Require().NoError(err, "listing %s's ReplicaSet", wl.Name)
	rsName := strings.TrimSpace(rsOut)
	s.Require().NotEmpty(rsName, "Deployment %s should own a ReplicaSet", wl.Name)

	conn := switchManagerSpec(t, ctx, deploymentsDisabledSpec(), ns)

	s.Eventually(func() bool {
		return workloadListed(conn.List(t), rsName, ns)
	}, workloadListPollTimeout, workloadListPollInterval,
		"ReplicaSet %s should show in list once Deployments are disabled", rsName)

	ls := s.LocalEcho()
	args := []string{
		"intercept", rsName, "--namespace", ns,
		"--port", fmt.Sprintf("%d:http", ls.Port()), "--mount", "false",
	}
	stdout, stderr, err := s.CLI().Run(ctx, args...)
	s.Require().NoError(err, "intercept: %s", stderr)
	s.Contains(stdout, fmt.Sprintf("Using %s %s", "ReplicaSet", rsName))

	s.True(workloadAttached(conn.List(t), rsName, ns),
		"list should show %s as intercepted", rsName)

	_, stderr, err = s.CLI().Run(ctx, "detach", rsName, "-n", ns)
	s.Require().NoError(err, "detach: %s", stderr)
}

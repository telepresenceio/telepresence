package attach

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	// argoNamespace is the private namespace ensureArgoRollouts installs the
	// Argo Rollouts controller into. Distinct from AppNamespace: it holds a
	// cluster component (a controller Deployment plus its RBAC), not a test
	// workload.
	argoNamespace = "rtest-argo"

	// argoInstallURL is the upstream Argo Rollouts install manifest: the
	// CRDs plus the namespaced controller (ServiceAccount, ClusterRole,
	// ClusterRoleBinding, Service, Deployment).
	argoInstallURL = "https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml"

	// argoControllerDeployment/argoClusterRoleBinding name the install.yaml
	// objects ensureArgoRollouts waits on and patches.
	argoControllerDeployment = "argo-rollouts"
	argoClusterRoleBinding   = "argo-rollouts"

	// rolloutWorkloadName is the one Rollout workload both tests provision
	// through the normal Workload fixture. WorkloadFixture memoizes by
	// (namespace, template), so both tests requesting this name/template
	// share the same underlying Rollout object rather than each creating
	// their own.
	rolloutWorkloadName = "attach-argo-rollout"
)

// argoListTimeout/argoListInterval bound how long a manager-side workload
// watcher (re)start takes to reach a `list` result: the manager spec switch
// in Test_InterceptsRollout rolls the pod, and either test's controller
// informer needs a moment to relist after ensureArgoRollouts' own rollout.
const (
	argoListTimeout  = 60 * time.Second
	argoListInterval = 2 * time.Second
)

// argoRolloutsSpec is managers.Default with workloads.argoRollouts.enabled
// forced to true (values.schema.yaml's workloads.argoRollouts default is
// false). Built inline rather than as a managers catalog entry, since only
// this suite needs it.
func argoRolloutsSpec() managers.Spec {
	enabled := true
	v := managers.Default.Values
	v.Workloads.ArgoRollouts.Enabled = &enabled
	return managers.Spec{Key: "argo-rollouts", Values: v}
}

// ArgoRollouts proves the Argo Rollout workload kind: attach support once
// workloads.argoRollouts is enabled, and the disabled default's fallback to
// reporting the underlying ReplicaSet Argo Rollouts always creates, instead
// of the Rollout itself (cmd/traffic/cmd/manager/state/watcher.go's
// hasValidReplicasetOwner: a ReplicaSet's controller-owner only counts once
// its kind is among the manager's EnabledWorkloadKinds, so a Rollout-owned
// ReplicaSet surfaces as its own entry whenever RolloutKind isn't enabled).
// Registered on managers.Default; each test Mutates to whichever spec it
// needs, so neither depends on the other having run first.
type ArgoRollouts struct {
	rt.Suite
	// argoReady is set once ensureArgoRollouts has installed the CRDs and
	// controller for this suite run. Suites run one at a time (see
	// fixture.go's engine), so a plain bool needs no synchronization.
	argoReady bool
}

func init() {
	rt.Register(&ArgoRollouts{},
		rt.InArea("attach"),
		rt.NeedsManager(managers.Default),
		rt.WithLabels(rt.Slow),
	)
}

// TearDownSuite deletes the private argo-rollouts namespace
// ensureArgoRollouts created, unconditionally: the same always-destroy rule
// the framework applies to its own private-namespace fixtures (never shared
// or adopted, so nothing gains from keeping it past this suite). The
// cluster-scoped Argo Rollouts CRDs are left in place: deleting shared CRDs
// from a dev cluster mid-run is riskier than leaving them.
func (s *ArgoRollouts) TearDownSuite() {
	if !s.argoReady {
		return
	}
	r := s.R()
	if _, err := r.Kubectl(s.Ctx(), "", "delete", "namespace", argoNamespace, "--ignore-not-found", "--wait=false"); err != nil {
		r.Infof("[rtest] ArgoRollouts: delete namespace %s: %v", argoNamespace, err)
	}
}

// ensureArgoRollouts installs the Argo Rollouts CRDs and controller, once
// per suite: neither test can create, let alone intercept, a Rollout
// without them. It is a suite-scoped installer, not a framework fixture --
// the CRDs it creates are cluster-scoped, so only its own namespace
// (argoNamespace) is suite-private and worth owning/cleaning up (see
// TearDownSuite).
//
// Reaching github.com for the manifest, and the controller's own image
// pull, both need cluster/host egress: the same property every other suite
// already assumes for its own image pulls.
func (s *ArgoRollouts) ensureArgoRollouts(t *testing.T) {
	t.Helper()
	if s.argoReady {
		return
	}
	r := s.R()
	ctx := s.Ctx()

	if _, err := r.Kubectl(ctx, "", "create", "namespace", argoNamespace); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "already exists") {
		t.Fatalf("create namespace %s: %v", argoNamespace, err)
	}

	if _, err := r.Kubectl(ctx, argoNamespace, "apply", "-f", argoInstallURL); err != nil {
		t.Fatalf("apply argo-rollouts install manifest: %v", err)
	}

	// install.yaml hardcodes its ClusterRoleBinding's subject to the
	// "argo-rollouts" namespace, assuming that's where it gets installed;
	// repoint it at argoNamespace, or the controller's own ServiceAccount
	// carries no RBAC and can never reconcile a Rollout.
	patch := fmt.Sprintf(`[{"op":"replace","path":"/subjects/0/namespace","value":%q}]`, argoNamespace)
	if _, err := r.Kubectl(ctx, "", "patch", "clusterrolebinding", argoClusterRoleBinding,
		"--type=json", "-p", patch); err != nil {
		t.Fatalf("patch %s clusterrolebinding namespace: %v", argoClusterRoleBinding, err)
	}

	if _, err := r.Kubectl(ctx, "", "wait", "--for=condition=established", "--timeout=120s",
		"crd/rollouts.argoproj.io"); err != nil {
		t.Fatalf("wait for rollouts.argoproj.io CRD: %v", err)
	}
	if _, err := r.Kubectl(ctx, argoNamespace, "rollout", "status",
		"deploy/"+argoControllerDeployment, "--timeout=120s"); err != nil {
		t.Fatalf("wait for argo-rollouts controller rollout: %v", err)
	}

	s.argoReady = true
}

// Test_InterceptsRollout proves the Rollout workload kind attaches like any
// other once workloads.argoRollouts is enabled: `list` reports it directly
// (cmd/traffic/cmd/manager/mutator/watcher.go's startInformers only starts
// the Rollout informer when the manager's EnabledWorkloadKinds includes
// k8sapi.RolloutKind), intercept names the workload kind in its output,
// traffic routes to the local echo while attached, and `uninstall` removes
// its agent the same way it does for any other workload kind (Uninstall,
// this package).
func (s *ArgoRollouts) Test_InterceptsRollout() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	s.ensureArgoRollouts(t)

	// A non-Default spec: acquired directly through the fixtures rather
	// than Suite.Manager/Suite.Connect, which resolve to the suite's
	// registered spec (Default) instead.
	rt.Mutate(t, rt.ManagerFixture(argoRolloutsSpec()))
	// The manager switch above rolled the release and invalidated every
	// connection (fixture_manager.go's provisionManager); this Mutate
	// re-provisions one fresh against the new manager pod.
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))

	wl := s.Workload(workloads.EchoRollout(rolloutWorkloadName))
	ls := s.LocalEcho()

	s.Require().Eventually(func() bool {
		return listContains(conn.List(t), wl.Name, wl.Namespace)
	}, argoListTimeout, argoListInterval, "list should eventually show the Rollout %s", wl.Name)

	// A raw, non-JSON invocation: --format json (Conn.Intercept's own path)
	// puts the progress writer in ModeQuiet
	// (pkg/client/cli/connect/init_command.go's InitProgressWriter), which
	// silences the "Using %s %s" line this assertion needs
	// (pkg/client/cli/intercept/state.go's create()).
	args := make([]string, 0, 12)
	args = append(args, "intercept", wl.Name, "--namespace", wl.Namespace)
	args = append(args, cli.MountFalse()()...)
	args = append(args, rt.ToLocal(ls, "http")()...)
	stdout, stderr, err := s.CLI().Run(ctx, args...)
	s.Require().NoError(err, "intercept: %s", stderr)
	s.Contains(stdout, "Using Rollout "+wl.Name, "intercept should report the Rollout workload kind")

	entries := conn.List(t)
	s.True(attachedInList(entries, wl.Name, wl.Namespace, "intercept"),
		"list should show %s as intercepted", wl.Name)

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	_, stderr, err = s.CLI().Run(ctx, "detach", wl.Name, "-n", wl.Namespace)
	s.Require().NoError(err, "detach: %s", stderr)
	s.False(attachedInList(conn.List(t), wl.Name, wl.Namespace, "intercept"),
		"list should no longer show %s as intercepted after detach", wl.Name)

	_, stderr, err = s.CLI().Run(ctx, "uninstall", wl.Name)
	s.Require().NoError(err, "uninstall: %s", stderr)
	// Generous timeout: uninstall evicts the workload's pods and the
	// replacement needs to schedule and settle before the agent snapshot
	// drops it, the same reap uninstall.go's Uninstall test waits out.
	s.Require().Eventually(func() bool {
		return !hasInstalledAgent(&s.Suite, wl)
	}, uninstallTimeout, uninstallPollInterval,
		"list --agents should no longer show %s.%s after uninstall", wl.Name, wl.Namespace)
}

// Test_ListsUnderlyingReplicaSetWhenDisabled proves the plain Default spec
// (workloads.argoRollouts disabled) never reports the Rollout by name:
// `list` instead shows the underlying ReplicaSet Argo Rollouts always
// creates, because the manager's ReplicaSet watcher stops treating the
// Rollout as that ReplicaSet's "valid" controller once RolloutKind is
// absent from EnabledWorkloadKinds, and reports the ReplicaSet directly
// instead (cmd/traffic/cmd/manager/state/watcher.go's
// hasValidReplicasetOwner).
func (s *ArgoRollouts) Test_ListsUnderlyingReplicaSetWhenDisabled() {
	t := s.T()
	s.ensureArgoRollouts(t)

	// Guarantees Default is the live spec even when Test_InterceptsRollout
	// Mutated the release away from it first in this process; SetupTest
	// already does this from the registered spec (managers.Default), this
	// just keeps the test self-contained to read.
	s.Manager()
	conn := s.Connect()

	wl := s.Workload(workloads.EchoRollout(rolloutWorkloadName))
	replicaSetPrefix := wl.Name + "-"

	var entries []cli.ListEntry
	s.Require().Eventually(func() bool {
		entries = conn.List(t)
		return hasEntryWithPrefix(entries, replicaSetPrefix, wl.Namespace)
	}, argoListTimeout, argoListInterval,
		"list should eventually show the ReplicaSet underlying %s", wl.Name)

	s.False(listContains(entries, wl.Name, wl.Namespace),
		"list should not show the Rollout %s itself while workloads.argoRollouts is disabled", wl.Name)
}

// hasEntryWithPrefix reports whether entries contains an entry in ns whose
// name starts with prefix.
func hasEntryWithPrefix(entries []cli.ListEntry, prefix, ns string) bool {
	for _, e := range entries {
		if e.Namespace == ns && strings.HasPrefix(e.Name, prefix) {
			return true
		}
	}
	return false
}

package connect

import (
	"context"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// managerStatefulSet is the chart-created StatefulSet name for the shared
// traffic-manager release (rt/fixture_manager.go's unexported
// helmReleaseName; the chart converted the manager from a Deployment to a
// single-replica StatefulSet so the client can dial its pod by a known
// name, traffic-manager-0).
const managerStatefulSet = "traffic-manager"

// rolloutAgentContainer is the traffic-agent's container name inside an
// injected pod (agentconfig.ContainerName), duplicated from injector/
// nodeagent's identically named consts: this package has no dependency on
// either.
const rolloutAgentContainer = "traffic-agent"

// rolloutRecoveryTimeout bounds every post-rollout Eventually poll: the
// client's reconnect (known-name pod dial, then session/intercept
// restoration) is asynchronous and a StatefulSet rollout plus a fresh pod
// becoming ready can itself take the better part of a minute.
// rolloutPollInterval is the poll tick between attempts.
const (
	rolloutRecoveryTimeout = 2 * time.Minute
	rolloutPollInterval    = 3 * time.Second
)

// ManagerRollout proves the client-rbac-minimization phase-3 contract from a
// live client's perspective: a connected session with an active intercept
// survives a real traffic-manager StatefulSet rollout. The client
// reconnects (dialing the known pod name, traffic-manager-0, before falling
// back to discovery), and the fresh manager restores exactly the prior
// state -- no duplicate intercept, no duplicate agent. This runs alongside
// the auth area's restore-review coverage (AuthGate/AuthEnforcing): what
// this suite adds is the end-to-end client view of the same restart.
//
// Labeled Slow: it waits through a full StatefulSet rollout plus the
// client's own multi-step, asynchronous recovery.
type ManagerRollout struct {
	rt.Suite
}

func init() {
	rt.Register(&ManagerRollout{}, rt.InArea("connect"), rt.NeedsManager(managers.Default), rt.WithLabels(rt.Slow))
}

func (s *ManagerRollout) Test_ManagerRolloutSurvivesWithIntercept() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	tp := s.CLI()

	wl := s.Workload(workloads.Echo("mgr-rollout"))
	ls := s.LocalEcho()
	conn := s.Connect()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())

	// The detach is the assertion that the client's session came back, not
	// just cleanup: it is the only step here that must reach the manager and
	// name the session, so it is refused until ReconnectClient has restored
	// it (the session id survives the restart, so nothing client-side
	// changes to reveal the gap). The polls below can all be answered from
	// the daemon's cached state, and are satisfied before the client has
	// even noticed the restart when the rollout is quick.
	defer a.DetachWithin(t, rolloutRecoveryTimeout, rolloutPollInterval)

	// Baseline: the intercept routes traffic before the rollout starts.
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	// Declared before the raw kubectl restart below, mirroring
	// quic/manager_outage.go: a later suite's Get must re-provision the
	// release (a helm upgrade that waits for the old pod to be fully gone)
	// rather than trust this test's manual restart to have left it in an
	// equivalent state.
	rt.Mutate(t, rt.ManagerFixture(managers.Default))

	mgrNS := managers.ManagerNamespace
	_, err := r.Kubectl(ctx, mgrNS, "rollout", "restart", "statefulset/"+managerStatefulSet)
	s.Require().NoError(err, "rollout restart statefulset/%s", managerStatefulSet)
	_, err = r.Kubectl(ctx, mgrNS, "rollout", "status", "statefulset/"+managerStatefulSet, "--timeout=120s")
	s.Require().NoError(err, "rollout status statefulset/%s", managerStatefulSet)

	// The client's own reconnect is asynchronous from here: every
	// assertion below polls rather than checking once.

	s.Require().Eventually(func() bool {
		return statusHealthy(ctx, tp)
	}, rolloutRecoveryTimeout, rolloutPollInterval,
		"telepresence status should report a healthy connection again after the manager rollout")

	// Exactly one intercept for wl: neither lost nor duplicated by the
	// fresh manager's restore review.
	s.Require().Eventually(func() bool {
		n, ok := interceptCount(ctx, tp, wl.Name, wl.Namespace)
		return ok && n == 1
	}, rolloutRecoveryTimeout, rolloutPollInterval,
		"list should show exactly one intercept for %s.%s after the rollout", wl.Name, wl.Namespace)

	// Traffic reaches the local listener again.
	check.EventuallyHTTP(t, wl.ServiceURL(), check.BodyContains(ls.Marker()), rolloutRecoveryTimeout)

	// No duplicate agent injection: exactly one traffic-agent container on
	// the workload's pod.
	s.Require().Eventually(func() bool {
		n, cerr := agentContainerCount(ctx, r, wl.Namespace, wl.Name)
		return cerr == nil && n == 1
	}, rolloutRecoveryTimeout, rolloutPollInterval,
		"pod for %s should carry exactly one traffic-agent container after the rollout", wl.Name)
}

// statusHealthy reports whether `telepresence status` currently shows a
// running user daemon connected to a traffic manager. Used only inside
// Eventually predicates: an error is expected while the client is still
// reconnecting and simply means "not yet", never a hard failure.
func statusHealthy(ctx context.Context, tp *cli.TP) bool {
	var st cli.Status
	if err := tp.JSON(ctx, &st, "status", "--format", "json"); err != nil {
		return false
	}
	return st.UserDaemon.Running && st.TrafficManager.Name != ""
}

// interceptCount returns how many intercepts `list` reports for the
// workload named name in ns, and whether the call itself succeeded (false
// while the daemon is still reconnecting).
func interceptCount(ctx context.Context, tp *cli.TP, name, ns string) (int, bool) {
	var entries []cli.ListEntry
	if err := tp.JSON(ctx, &entries, "list", "--format", "json", "-n", ns); err != nil {
		return 0, false
	}
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return len(e.InterceptInfo), true
		}
	}
	return 0, true
}

// agentContainerCount returns how many containers named "traffic-agent"
// exist across the pods matching the app=name selector in ns.
func agentContainerCount(ctx context.Context, r *rt.Runtime, ns, name string) (int, error) {
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+name, "-o",
		"jsonpath={.items[*].spec.containers[*].name}")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, f := range strings.Fields(out) {
		if f == rolloutAgentContainer {
			count++
		}
	}
	return count, nil
}

package connect

import (
	"context"
	"net/netip"
	"runtime"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// managerStatefulSet is the chart-created StatefulSet name for the shared
// traffic-manager release.
const managerStatefulSet = "traffic-manager"

// rolloutAgentContainer is the traffic-agent's container name inside an
// injected pod.
const rolloutAgentContainer = "traffic-agent"

// rolloutRecoveryTimeout/rolloutPollInterval bound every post-rollout
// Eventually poll: a StatefulSet rollout plus client reconnect can take the
// better part of a minute.
const (
	rolloutRecoveryTimeout = 2 * time.Minute
	rolloutPollInterval    = 3 * time.Second
)

// ManagerRollout proves that a connected session with an active intercept
// survives a real traffic-manager StatefulSet rollout, reconnecting with no
// duplicate intercept and no duplicate agent.
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
	var oldAddr string
	if runtime.GOOS == "linux" {
		oldAddr = s.managerPodAddr(ctx, r, mgrNS)
	}
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

	// The daemon's cluster DNS address moves from the old traffic-manager pod to
	// the new one. Neither address may get a host route outside the virtual interface.
	if runtime.GOOS == "linux" {
		newAddr := s.managerPodAddr(ctx, r, mgrNS)
		s.Require().Eventually(func() bool {
			return rootDNSAddr(ctx, tp) == newAddr
		}, rolloutRecoveryTimeout, rolloutPollInterval,
			"the root daemon should use the new manager pod address %s as its DNS address", newAddr)
		for _, addr := range []string{oldAddr, newAddr} {
			s.Require().Never(func() bool { return len(routesAround(ctx, addr)) > 0 }, 5*time.Second, 500*time.Millisecond,
				"no host route may exist for %s: %v", addr, routesAround(ctx, addr))
		}
	}

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

// statusHealthy reports whether `telepresence status` shows a running user
// daemon connected to a traffic manager.
func statusHealthy(ctx context.Context, tp *cli.TP) bool {
	var st cli.Status
	if err := tp.JSON(ctx, &st, "status", "--format", "json"); err != nil {
		return false
	}
	return st.UserDaemon.Running && st.TrafficManager.Name != ""
}

// managerPodAddr returns the address of the traffic-manager pod.
func (s *ManagerRollout) managerPodAddr(ctx context.Context, r *rt.Runtime, ns string) string {
	out, err := r.Kubectl(ctx, ns, "get", "pod", managerStatefulSet+"-0", "-o", "jsonpath={.status.podIP}")
	s.Require().NoError(err, "get the manager pod's address")
	addr := strings.TrimSpace(out)
	s.Require().NotEmpty(addr)
	return addr
}

// rootDNSAddr returns the address part of the root daemon's DNS address, or
// an empty string when it cannot be read.
func rootDNSAddr(ctx context.Context, tp *cli.TP) string {
	var st struct {
		RootDaemon struct {
			DNS struct {
				VifAddress string `json:"vif_address"`
			} `json:"dns"`
		} `json:"root_daemon"`
	}
	if err := tp.JSON(ctx, &st, "status", "--format", "json"); err != nil {
		return ""
	}
	ap, err := netip.ParseAddrPort(st.RootDaemon.DNS.VifAddress)
	if err != nil {
		return ""
	}
	return ap.Addr().String()
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

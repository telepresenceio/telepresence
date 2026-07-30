package attach

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// wiretapHeaderKey/wiretapHeaderVal is the filter every wiretap cell in
// this package uses. A filter isn't required to place a wiretap at all
// (cmd/traffic/cmd/manager/state/intercept.go:369 exempts wiretaps from
// every conflict check regardless of filters, and
// integration_test/wiretap_test.go's Test_MultipleTapsOnOnePort taps
// unfiltered), but using one here lets each cell prove the documented
// pass-through semantic: a request carrying the header still reaches the
// cluster, because a wiretap copies traffic rather than diverting it
// (integration_test/wiretap_test.go's Test_HTTPFilteredWiretap).
const (
	wiretapHeaderKey = "x-rtest-wiretap"
	wiretapHeaderVal = "match"
)

// runCell attaches verb to a workload rendered from tpl, asserts list shows
// it, and (per verb) checks routing before and, for intercept/replace,
// after detach.
//
// tpl.NoService workloads have no cluster Service and hence no URL for
// check.EventuallyHTTP to probe, so their cells exercise attach/list/detach
// only; this is a limitation of this suite's URL-based routing checks, not
// of the CLI, which attaches to service-less workloads just fine (see
// integration_test/workloads_test.go's
// Test_SuccessfullyIntercepts/IngestsDeploymentWithoutService). ingest
// never touches traffic (pkg/client/userd/trafficmgr/ingest.go's Ingest
// RPC never registers a port with the manager), so it never gets a routing
// check either, regardless of tpl.
func runCell(s *rt.Suite, verb string, tpl workloads.Template) {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(tpl)
	ls := s.LocalEcho()

	var a *rt.Attach
	switch verb {
	case "intercept":
		a = conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	case "ingest":
		a = conn.Ingest(t, wl, cli.MountFalse())
	case "replace":
		a = conn.Replace(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
		s.True(a.Replace != nil && a.Replace.Replace, "replace response should report replace=true")
	case "wiretap":
		a = conn.Wiretap(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(),
			cli.HTTPHeader(wiretapHeaderKey, wiretapHeaderVal))
		s.True(a.Wiretap != nil && a.Wiretap.Wiretap, "wiretap response should report wiretap=true")
	default:
		t.Fatalf("unknown verb %q", verb)
	}

	s.True(listContains(conn.List(t), wl.Name, wl.Namespace), "list should show %s.%s", wl.Name, wl.Namespace)

	if !tpl.NoService {
		switch verb {
		case "intercept", "replace":
			if verb == "replace" {
				// Replace restarts the pod to swap the app container out;
				// wait for the rollout so the routing probe doesn't race the
				// respawn (single-replica statefulsets briefly serve nothing).
				waitRollout(s, wl)
			}
			rt.RoutedToLocal(t, wl.ServiceURL(), ls)
		case "wiretap":
			// A wiretap copies traffic, it never diverts it: the cluster
			// keeps serving header-matching requests while tapped.
			rt.RoutedToCluster(t, wl.ServiceURL(), check.WithHeader(wiretapHeaderKey, wiretapHeaderVal))
		}
	}

	if verb == "intercept" && len(tpl.ExtraPorts) > 0 {
		// Multiport: the "http" port above is the only one intercepted;
		// the other named port must keep serving the cluster.
		other := tpl.ExtraPorts[0].Name
		otherURL, ok := wl.ServiceURLNamed(other)
		s.True(ok, "workload should expose port %s", other)
		rt.RoutedToCluster(t, otherURL)
	}

	a.Detach(t)

	if !tpl.NoService && (verb == "intercept" || verb == "replace") {
		if verb == "replace" {
			waitRollout(s, wl)
		}
		rt.RoutedToCluster(t, wl.ServiceURL())
	}
}

// waitRollout waits for wl's rollout to settle: replace swaps the app
// container in and out by restarting the pod, and single-replica workloads
// briefly serve nothing while that happens.
func waitRollout(s *rt.Suite, wl *rt.Workload) {
	kindPath := strings.ToLower(wl.Kind) + "/" + wl.Name
	if _, err := s.R().Kubectl(s.Ctx(), wl.Namespace, "rollout", "status", kindPath, "--timeout=120s"); err != nil {
		s.T().Fatalf("rollout %s: %v", kindPath, err)
	}
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

// Package intercept holds suites that exercise intercept-specific behavior
// beyond plain attach/detach, such as request filters.
package intercept

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// headerKey/headerVal is the filter this suite intercepts on.
const (
	headerKey = "x-rtest-filter"
	headerVal = "match"
)

// filterPathPrefix is the --http-path-prefix filter Test_PathPrefix and
// Test_Combined intercept on.
const filterPathPrefix = "/api"

// coexistHeaderA/B route Test_Coexistence's two simultaneous filtered
// intercepts on one workload to two different local services.
const (
	coexistHeaderA = "coexist-a"
	coexistHeaderB = "coexist-b"
)

// HeaderFilter proves that an --http-header intercept filter routes real
// traffic selectively: a request carrying the header reaches the local
// service, one without it keeps reaching the cluster.
type HeaderFilter struct {
	rt.Suite
}

func init() {
	rt.Register(&HeaderFilter{},
		rt.InArea("intercept"),
		rt.NeedsManager(managers.Default),
		rt.WithLabels(rt.CompatCore),
	)
}

// Test_Header attaches a header-filtered intercept, then sends one request
// with the filter header (expecting the local marker) and one without
// (expecting the cluster's own response).
func (s *HeaderFilter) Test_Header() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("header-filter"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl,
		rt.ToLocal(ls, "http"),
		cli.MountFalse(),
		cli.HTTPHeader(headerKey, headerVal),
	)
	defer a.Detach(t)

	url := wl.ServiceURL()
	rt.RoutedToLocal(t, url, ls, check.WithHeader(headerKey, headerVal))
	rt.RoutedToCluster(t, url)
}

// Test_PathPrefix attaches a path-prefix-filtered intercept, then sends one
// request under the prefix (expecting the local marker) and one outside it
// (expecting the cluster's own response).
func (s *HeaderFilter) Test_PathPrefix() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("path-prefix"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl,
		rt.ToLocal(ls, "http"),
		cli.MountFalse(),
		cli.HTTPPathPrefix(filterPathPrefix),
	)
	defer a.Detach(t)

	base := wl.ServiceURL()
	rt.RoutedToLocal(t, base+filterPathPrefix+"/widget", ls)
	rt.RoutedToCluster(t, base+"/other")
}

// Test_Combined attaches an intercept filtered on both a header and a path
// prefix, then proves both must match: header+path routes local, either one
// alone still routes to the cluster.
func (s *HeaderFilter) Test_Combined() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("combined-filter"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl,
		rt.ToLocal(ls, "http"),
		cli.MountFalse(),
		cli.HTTPHeader(headerKey, headerVal),
		cli.HTTPPathPrefix(filterPathPrefix),
	)
	defer a.Detach(t)

	base := wl.ServiceURL()
	matchURL := base + filterPathPrefix + "/widget"
	otherURL := base + "/other"

	rt.RoutedToLocal(t, matchURL, ls, check.WithHeader(headerKey, headerVal))
	rt.RoutedToCluster(t, matchURL)                                         // path matches, header missing
	rt.RoutedToCluster(t, otherURL, check.WithHeader(headerKey, headerVal)) // header matches, path doesn't
}

// Test_Coexistence attaches two header-filtered intercepts to the same
// workload+port, each with a distinct name and header value routing to a
// distinct local service, and proves both serve their own traffic while
// unmatched requests still reach the cluster. Conn.Intercept always derives
// the intercept's positional name from the workload's name, so two
// intercepts sharing one workload need namedIntercept instead (distinct
// name, common --workload).
func (s *HeaderFilter) Test_Coexistence() {
	t := s.T()
	s.Connect()
	wl := s.Workload(workloads.Echo("coexist"))
	lsA := s.LocalEcho()
	lsB := s.LocalEcho()
	tp, ctx := s.CLI(), s.Ctx()

	nameA, nameB := wl.Name+"-a", wl.Name+"-b"
	if stdout, stderr, err := namedIntercept(t, tp, ctx, wl, nameA,
		rt.ToLocal(lsA, "http"), cli.MountFalse(), cli.HTTPHeader(headerKey, coexistHeaderA)); err != nil {
		t.Fatalf("intercept %s: %v\nstdout:\n%s\nstderr:\n%s", nameA, err, stdout, stderr)
	}
	defer detachNamed(t, tp, ctx, nameA, wl.Namespace)

	if stdout, stderr, err := namedIntercept(t, tp, ctx, wl, nameB,
		rt.ToLocal(lsB, "http"), cli.MountFalse(), cli.HTTPHeader(headerKey, coexistHeaderB)); err != nil {
		t.Fatalf("intercept %s: %v\nstdout:\n%s\nstderr:\n%s", nameB, err, stdout, stderr)
	}
	defer detachNamed(t, tp, ctx, nameB, wl.Namespace)

	url := wl.ServiceURL()
	rt.RoutedToLocal(t, url, lsA, check.WithHeader(headerKey, coexistHeaderA))
	rt.RoutedToLocal(t, url, lsB, check.WithHeader(headerKey, coexistHeaderB))
	rt.RoutedToCluster(t, url)
}

// Test_TCPPortConflict proves that a second, unfiltered intercept on the
// same workload+port as an existing one is rejected: an unfiltered intercept
// would intercept all traffic, which always conflicts with any other
// intercept on the same port (pkg/icept/conflicts.go's IsInConflict, rule 1).
func (s *HeaderFilter) Test_TCPPortConflict() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("tcp-conflict"))
	ls1 := s.LocalEcho()
	ls2 := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls1, "http"), cli.MountFalse())
	defer a.Detach(t)

	tp, ctx := s.CLI(), s.Ctx()
	name2 := wl.Name + "-2"
	stdout, stderr, err := namedIntercept(t, tp, ctx, wl, name2, rt.ToLocal(ls2, "http"), cli.MountFalse())
	if err == nil {
		detachNamed(t, tp, ctx, name2, wl.Namespace)
		t.Fatalf("expected an unfiltered second intercept on the same port to fail with a conflict")
	}
	// --format json puts the error on stdout, not stderr; check both.
	combined := strings.ToLower(stdout + stderr)
	if !strings.Contains(combined, "conflict") {
		t.Fatalf("expected a conflict error, got stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// Package intercept holds suites that exercise intercept-specific behavior
// beyond plain attach/detach, such as request filters.
package intercept

import (
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

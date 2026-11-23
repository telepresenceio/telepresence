package intercept

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// dialpolTargets proves that every local destination an intercept declares
// is dialable by the traffic-agents that deliver to it: each additional pod
// port of a multi-port intercept, and each workload that shares a Service
// intercept.
type dialpolTargets struct {
	rt.Suite
}

func init() {
	rt.Register(&dialpolTargets{},
		rt.InArea("intercept"),
		rt.NeedsManager(managers.Default),
	)
}

// Test_AdditionalPodPorts intercepts both ports of a two-port workload, each
// mapped to its own local listener, and asserts that each service port
// reaches its own listener.
func (s *dialpolTargets) Test_AdditionalPodPorts() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoMultiPort("dialpol-ports"))
	first := s.LocalEcho()
	second := s.LocalEcho()

	a := conn.Intercept(t, wl,
		rt.ToLocal(first, "http"),
		cli.Port(second.Port(), "http2"),
		cli.MountFalse(),
	)
	defer a.Detach(t)

	secondURL, ok := wl.ServiceURLNamed("http2")
	if !ok {
		t.Fatalf("workload %s: no http2 port", wl.Name)
	}
	rt.RoutedToLocal(t, wl.ServiceURL(), first)
	rt.RoutedToLocal(t, secondURL, second)
}

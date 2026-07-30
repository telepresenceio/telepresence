package intercept

import (
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// InterceptRouting proves routing behavior across replica counts, multiple
// service ports, the localShortcut config on/off, and h2c preservation.
type InterceptRouting struct {
	rt.Suite
}

func init() {
	rt.Register(&InterceptRouting{},
		rt.InArea("intercept"),
		rt.NeedsManager(managers.Default),
	)
}

// multiReplicaRequests is how many sequential requests Test_MultiReplica
// sends: enough to very likely have hit every one of the workload's
// replicas at least once, were routing per-replica instead of
// intercept-wide.
const multiReplicaRequests = 20

// Test_MultiReplica proves a global intercept on a multi-replica workload
// routes every request to the local service, regardless of which replica
// would otherwise have served it (regression #4085).
func (s *InterceptRouting) Test_MultiReplica() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoReplicas("multi-echo", 4))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)

	url := wl.ServiceURL()
	for i := 0; i < multiReplicaRequests; i++ {
		rt.RoutedToLocal(t, url, ls)
	}
}

// Test_MultiPort proves an intercept on one named service port leaves the
// workload's other named port serving the cluster.
func (s *InterceptRouting) Test_MultiPort() {
	t := s.T()
	conn := s.Connect()
	wl := s.Workload(workloads.EchoMultiPort("multi-port"))
	ls := s.LocalEcho()

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)

	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	second, ok := wl.ServiceURLNamed("http2")
	if !ok {
		t.Fatalf("workload %s: no http2 port", wl.Name)
	}
	rt.RoutedToCluster(t, second)
}

// enableLocalShortcut and disableLocalShortcut are ConnWithConfig deltas for
// Test_LocalShortcut's config-variant connection pair. disableLocalShortcut
// matches the run's baseline (see Runtime.baselineConfig) but, going
// through ConnWithConfig, gets its own private connection fixture that
// nothing else in the run shares.
func enableLocalShortcut(c client.Config) {
	ic := c.Intercept()
	ic.LocalShortcut = true
	ic.LocalShortcutIsGlobal = true
}

func disableLocalShortcut(c client.Config) {
	ic := c.Intercept()
	ic.LocalShortcut = false
	ic.LocalShortcutIsGlobal = false
}

// Test_LocalShortcut proves intercept.localShortcut's effect on filtered
// intercepts: with it on and global, the root daemon routes every request
// from this host straight to the local handler, filter or not; with it off
// (the baseline), a non-matching request still reaches the cluster. Both
// connections go through rt.Mutate: they share the host's single daemon
// slot, so acquiring one quits and restarts the other under its own config
// dir (see fixture_connection.go's ensureHostConfigDir).
func (s *InterceptRouting) Test_LocalShortcut() {
	t := s.T()
	s.Manager()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("local-shortcut"))
	ls := s.LocalEcho()
	url := wl.ServiceURL()

	on := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(enableLocalShortcut)))
	a := on.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), cli.HTTPHeader(headerKey, headerVal))
	rt.RoutedToLocal(t, url, ls) // no header: the global shortcut takes it anyway
	a.Detach(t)

	off := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(disableLocalShortcut)))
	b := off.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), cli.HTTPHeader(headerKey, headerVal))
	defer b.Detach(t)
	rt.RoutedToLocal(t, url, ls, check.WithHeader(headerKey, headerVal))
	rt.RoutedToCluster(t, url)
}

// Test_H2C would prove that an h2c (HTTP/2 prior-knowledge) request to an
// intercepted service reaches a local h2c server. It can't be implemented
// yet: workloads.Template has no way to declare `appProtocol:
// kubernetes.io/h2c` on the generated Service port, which the agent needs
// to preserve h2c framing instead of downgrading to HTTP/1.1 (see
// integration_test/h2c_intercept_test.go). API gap.
func (s *InterceptRouting) Test_H2C() {
	s.T().Skip("api gap: workloads.Template lacks appProtocol support, needed to " +
		"declare kubernetes.io/h2c on the service port")
}

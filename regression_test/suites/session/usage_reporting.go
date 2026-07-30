package session

import (
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// usageReportTimeout/usageReportInterval bound the poll for reports: the
// manager's usage sender flushes periodically (every 30s as of this
// writing), so the timeout leaves ample margin.
const (
	usageReportTimeout  = 90 * time.Second
	usageReportInterval = 3 * time.Second
)

// UsageReporting proves the traffic-manager and a connected client both send
// usage reports to a collector reachable from the cluster, once the shared
// release is pointed at it (managers.UsageTo) and the client's own config
// also points usage at it. Ported from usage_reporting_test.go, whose skip
// probe (host.docker.internal unresolvable from the cluster)
// rt.ProbeUsageCollectorReachable mirrors exactly.
//
// It declares no NeedsManager: the collector's address is only known once
// rt.NewUsageCollector has bound a port at runtime, so the manager spec
// (managers.UsageTo(addr)) is acquired in-test via rt.Mutate, not at
// Register time.
type UsageReporting struct {
	rt.Suite
}

func init() {
	rt.Register(&UsageReporting{}, rt.InArea("session"))
}

func (s *UsageReporting) Test_ReportedFromClientAndManager() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	// Ensure a baseline release (and its namespace) exist before probing:
	// this suite declares no NeedsManager, so nothing else guarantees it.
	s.Manager()

	collector, err := rt.NewUsageCollector()
	s.Require().NoError(err)
	t.Cleanup(collector.Close)

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	addr, ok, reason := rt.ProbeUsageCollectorReachable(env, managers.ManagerNamespace, collector)
	if !ok {
		t.Skip(reason)
	}

	freeDefaultConnection(t, ns)
	rt.Mutate(t, rt.ManagerFixture(managers.UsageTo(addr)))

	wl := s.Workload(workloads.Echo("usage-echo"))
	ls := s.LocalEcho()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnWithConfig(func(cfg client.Config) {
		u := cfg.Usage()
		u.Enabled = true
		u.CollectorAddress = collector.LocalAddr()
		u.Insecure = true
	})))

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	a.Detach(t)

	s.Eventually(func() bool {
		var sawClient, sawManager bool
		for _, r := range collector.Reports() {
			switch r.GetSource() {
			case "client":
				sawClient = true
			case "manager":
				sawManager = true
			}
		}
		return sawClient && sawManager
	}, usageReportTimeout, usageReportInterval,
		"did not receive usage reports from both client and manager")

	// Never leave the collector-config connection running: quit it now, on
	// top of the Mutate above, so a later area's Get(ManagerFixture(...)) /
	// Get(ConnectionFixture(...)) re-provisions instead of adopting this
	// spec's leftovers.
	conn.Disconnect(t)
}

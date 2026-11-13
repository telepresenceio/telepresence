package attach

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// slowReadySeconds is how long the workload's pods stay not Ready. It must
// comfortably exceed the detach, quit, connect and second replace.
const slowReadySeconds = 15

// ReplaceReconnect proves that a client that reconnects while a replaced
// pod is still not Ready learns about that pod when a new replace of the
// same workload reuses it.
type ReplaceReconnect struct {
	rt.Suite
}

func init() {
	rt.Register(&ReplaceReconnect{},
		rt.InArea("attach"),
		rt.NeedsManager(managers.Default),
	)
}

// Test_ReplaceAfterReconnectWhilePodNotReady replaces a workload whose pods
// stay not Ready, detaches, quits and reconnects, and replaces again, all
// within the not-Ready window. The second replace must succeed.
func (s *ReplaceReconnect) Test_ReplaceAfterReconnectWhilePodNotReady() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()

	// The connection is quit and re-established, so later tests must get a
	// fresh one.
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	tpl := workloads.Echo("replace-reconnect")
	tpl.ReadyDelaySeconds = slowReadySeconds
	wl := s.Workload(tpl)
	ls := s.LocalEcho()
	opts := []cli.InterceptOpt{rt.ToLocal(ls, "http"), cli.MountFalse(), cli.Container(wl.Name)}

	a := conn.Replace(t, wl, opts...)
	a.Detach(t)

	_, stderr, err := s.CLI().Run(ctx, "quit")
	s.Require().NoError(err, "quit: %s", stderr)
	conn = rt.Reconnect(t, ctx, ns)

	a = conn.Replace(t, wl, opts...)
	a.Detach(t)
}

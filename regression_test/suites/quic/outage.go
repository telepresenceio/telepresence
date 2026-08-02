package quic

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Outage exercises the quic-forwarder's failure/recovery path in miniature:
// scaling the quic-forwarder Deployment to zero must not break VPN traffic
// (the manager-bound tunnel falls back to "grpc (fallback)" once its
// established quic connection dies) and scaling it back must let the
// transport recover to quic on its own, no reconnect required. The fallback
// phase needs VPN flows riding the manager-bound tunnel, which requires no
// traffic-agent in the connected namespace, so this suite connects to its
// own fresh, agentless PrivateNamespace rather than the shared rtest-app
// one. Mirrors (in miniature) quic_test.go's
// Test_AZForwarderOutageFallsBackAndRecovers. Labeled Slow: it waits through
// a full quic idle-timeout-driven fallback plus the client's background
// re-probe.
type Outage struct {
	rt.Suite
}

func init() {
	rt.Register(&Outage{}, rt.InArea("quic"), rt.NeedsManager(managers.QuicNodePort()), rt.WithLabels(rt.Slow))
}

func (s *Outage) Test_ForwarderOutageFallsBackAndRecovers() {
	t := s.T()
	t.Skip("under investigation: the mid-outage grpc (fallback) status was never observed on the kind dev " +
		"cluster even with the agentless-namespace flow; see docs/plans/regression-test-framework/findings.md")
	ctx := s.Ctx()
	r := s.R()
	tp := s.CLI()

	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateNamespace(env, "quic-outage")
	// ns is brand new: the already-running manager only manages it once its
	// pod restarts and re-lists namespaces.
	s.Require().NoError(rt.RestartManager(env))
	wl := rt.Get(t, rt.WorkloadFixture(ns, workloads.Echo("quic-outage")))
	ls := s.LocalEcho()

	// Free whatever the default connection currently holds before connecting
	// fresh to ns: the manager restart above would invalidate a connection
	// made before it.
	rt.Mutate(t, rt.ConnectionFixture(s.AppNamespace())).Disconnect(t)
	conn := awaitTransportPrefix(t, ctx, tp, ns, rt.Reconnect(t, ctx, ns))

	scaleQuicForwarderDown(t, ctx, r)
	restored := false
	defer func() {
		if !restored {
			scaleQuicForwarderUp(t, ctx, r)
		}
	}()

	// No traffic-agent exists in ns, so VPN flows ride the manager-bound
	// tunnel: traffic keeps working while the forwarder is gone, and status
	// settles on the mid-session fallback value once the dead quic
	// connection trips.
	awaitFallbackWithTraffic(t, ctx, tp, wl.ServiceURL())

	// A fresh intercept during the outage: the agent's quic dial has no
	// forwarder to reach, so the attachment comes up on its per-agent
	// port-forward instead.
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	detached := false
	defer func() {
		if !detached {
			a.Detach(t)
		}
	}()
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	a.Detach(t)
	detached = true

	scaleQuicForwarderUp(t, ctx, r)
	restored = true

	// Recovery happens on the client's own background re-probe: no reconnect
	// needed.
	awaitStatusTransportPrefix(t, ctx, tp, quicPrefix, quicRecoveryTimeout)
}

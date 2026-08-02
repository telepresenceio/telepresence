package quic

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// quicDatagramsEnvVar is the manager-process opt-in for RFC 9221 datagram
// carriage on the quic tunnel transport (cmd/traffic/cmd/manager/
// quictunnel/listener.go), OFF by default -- no better than stream carriage,
// see "Current limitations" in docs/reference/quic-transport.md. Same
// variable integration_test/quic_test.go's Test_AUDPEchoDatagrams set on
// deploy/traffic-manager.
const quicDatagramsEnvVar = "TELEPRESENCE_QUIC_ENABLE_DATAGRAMS"

// quicDatagramsSpec is managers.QuicNodePort() with the datagrams opt-in
// layered on top via the chart's extraEnv passthrough
// (managers.Values.ExtraEnv) -- the sanctioned inline-Spec pattern this area
// already uses for a forced-endpoint overlay (fallback.go's
// quicUnreachableSpec). Built inline (managers.Spec{Key: "quic-datagrams",
// ...}) rather than as a separate managers catalog entry, since no other
// suite needs it. The spec starts the manager process with the opt-in
// already set rather than toggling it mid-suite, so the datagram counters
// this suite asserts on start at zero for the whole release, with no risk
// of a stale nonzero count left by an earlier test.
func quicDatagramsSpec() managers.Spec {
	v := managers.QuicNodePort().Values
	v.ExtraEnv = append(v.ExtraEnv, corev1.EnvVar{Name: quicDatagramsEnvVar, Value: "true"})
	// The periodic counters line this suite asserts on is a clog.Debugf
	// (cmd/traffic/cmd/manager/quictunnel/listener.go, logDatagramStatsLoop),
	// invisible at the chart's default log level.
	v.LogLevel = "debug"
	return managers.Spec{Key: "quic-datagrams", Values: v}
}

// Datagrams proves RFC 9221 datagram carriage actually carries real
// tunneled UDP traffic once opted into: a UDP echo round-trips over the
// quic transport and the manager's own periodic datagram-counters log line
// (cmd/traffic/cmd/manager/quictunnel/listener.go's logDatagramStatsLoop)
// reports a nonzero received count. Supersedes quic_test.go's
// Test_AUDPEchoDatagrams.
//
// Only manager-bound flows can ride datagrams: once any traffic-agent is
// reachable from the session, agentpf routes destinations through the
// client-to-agent path instead, which never negotiates datagrams
// (stream_creator.go's AttachDatagramRoute comment). Not attaching is not
// enough to guarantee that: a shared dev-mode cluster keeps agents from
// earlier areas and runs alive, and any of them silently serves this
// test's UDP flow. The test therefore runs in its own private namespace
// with its own connection, where no agent can exist.
//
// Labeled Slow: the round trip plus waiting on the manager's periodic log
// line both take real wall-clock time.
type Datagrams struct {
	rt.Suite
}

func init() {
	rt.Register(&Datagrams{}, rt.InArea("quic"), rt.NeedsManager(quicDatagramsSpec()), rt.WithLabels(rt.Slow))
}

func (s *Datagrams) Test_UDPEchoRoundTrip() {
	t := s.T()
	ctx := s.Ctx()
	tp := s.CLI()
	r := s.R()

	env := rt.Env{Ctx: ctx, T: t, R: r}
	ns := rt.PrivateNamespace(env, "quic-datagrams")
	// ns is brand new: the already-running manager only manages it once its
	// pod restarts and re-lists namespaces.
	s.Require().NoError(rt.RestartManager(env))
	wl := rt.Get(t, rt.WorkloadFixture(ns, workloads.UDPEcho("quic-datagrams")))

	// Free whatever the default connection currently holds before
	// connecting fresh to ns: the manager restart above would invalidate a
	// connection made before it. The session maps only ns so that no agent
	// in any other namespace is visible to it: one reachable agent anywhere
	// would serve the UDP flow over the never-datagram agent path.
	rt.Mutate(t, rt.ConnectionFixture(s.AppNamespace())).Disconnect(t)
	mapped := rt.ConnExtraArgs("--mapped-namespaces", ns)
	awaitTransportPrefix(t, ctx, tp, ns, rt.Reconnect(t, ctx, ns, mapped), mapped)

	udpEchoRoundTrip(t, ctx, wl, "ping over quic datagrams")

	// The transport must still be quic after the round trip -- the counters
	// assertion below is only meaningful while datagrams actually carried
	// this test's own traffic.
	st := fetchStatus(t, ctx, tp)
	s.True(strings.HasPrefix(st.RootDaemon.TunnelTransport, quicPrefix),
		"status should still report the quic transport after the echo round trip, got %q", st.RootDaemon.TunnelTransport)

	awaitNonzeroDatagramsReceived(t, ctx, r)
}

package quic

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// quicUnreachableHost is an RFC 5737 TEST-NET-1 address: guaranteed
// non-routable, so a quicTunnel.externalHost pointed at it can never be
// reached.
const quicUnreachableHost = "192.0.2.1"

// quicUnreachableSpec is managers.QuicNodePort() with an unreachable,
// explicitly forced advertised endpoint layered on top
// (quicTunnel.externalHost/externalPort), overriding the self-discovered
// endpoint QuicNodePort's own doc comment describes -- exactly the escape
// hatch that comment names for a suite needing a specific, forced endpoint.
// Built inline (managers.Spec{Key: "quic-unreachable", ...}) rather than as
// a separate managers catalog entry, since no other suite needs it.
func quicUnreachableSpec() managers.Spec {
	v := managers.QuicNodePort().Values
	v.QuicTunnel.ExternalHost = quicUnreachableHost
	v.QuicTunnel.ExternalPort = managers.QuicNodePortPort
	return managers.Spec{Key: "quic-unreachable", Values: v}
}

// Fallback proves the silent-fallback property for an endpoint that is
// advertised but never reachable: connect succeeds, status settles on the
// plain "grpc" transport (never "grpc (fallback)" -- an initial dial that
// fails quietly is not a fallback event), and traffic still works.
type Fallback struct {
	rt.Suite
}

func init() {
	rt.Register(&Fallback{}, rt.InArea("quic"), rt.NeedsManager(quicUnreachableSpec()))
}

func (s *Fallback) Test_SilentlyFallsBackToGRPC() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	tp := s.CLI()
	wl := s.Workload(workloads.Echo("quic-fallback"))

	freshConnect(t, ctx, ns)

	awaitStatusTransport(t, ctx, tp, "grpc", quicStatusTimeout)
	rt.RoutedToCluster(t, wl.ServiceURL())
}

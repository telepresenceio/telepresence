package quic

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// ForwarderRestart exercises the forwarder's central failure-mode claim from
// "The forwarder" section of docs/reference/quic-transport-architecture.md:
// killing the stateless packet router must not force a permanent fallback to
// the gRPC transport. With the connection already established and the
// manager-bound tunnel on quic, it deletes every quic-forwarder pod and
// requires traffic to recover -- while "telepresence status" reports the
// quic transport throughout, never grpc.
//
// Either the client's QUIC connection survives the restart outright (CID
// routing plus path validation to the replacement pod's new address) or, at
// worst, traffic stalls until the client's keep-alives establish a fresh
// flow through the new pod (kube-proxy's UDP conntrack can keep pinning the
// old flow to the now-gone pod IP for a while). Either way tunnel_transport
// must never flip to grpc: that would mean the client gave up on quic
// instead of riding out the forwarder restart, which is exactly what a
// stateless forwarder is supposed to make unnecessary.
type ForwarderRestart struct {
	rt.Suite
}

func init() {
	rt.Register(&ForwarderRestart{}, rt.InArea("quic"), rt.NeedsManager(managers.QuicNodePort()))
}

func (s *ForwarderRestart) Test_TrafficSurvivesForwarderPodDeletion() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	tp := s.CLI()
	r := s.R()
	wl := s.Workload(workloads.Echo("quic-forwarder-restart"))

	awaitTransportPrefix(t, ctx, tp, ns, freshConnect(t, ctx, ns))

	deleteQuicForwarderPods(t, ctx, r)

	// The stateless-router property under test is that the transport rides
	// out the forwarder's replacement without ever falling back, not just
	// that it eventually recovers.
	awaitRecoveryNeverLeavingQuic(t, ctx, tp, wl.ServiceURL())

	// A final, non-Eventually round trip: recovery isn't just a momentarily
	// true poll result, plain traffic through the tunnel keeps working.
	rt.RoutedToCluster(t, wl.ServiceURL())
}

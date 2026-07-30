package quic

import (
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Disabled proves the other half of the fallback story quic_disabled_test.go
// covers: a traffic-manager installed without quicTunnel.enabled (the
// chart's own default, managers.Default) keeps serving the tunnel over
// plain gRPC -- never "grpc (fallback)", since there was never a quic
// endpoint to fall back from -- and "telepresence status" says so, with a
// regular intercept still round-tripping over it.
type Disabled struct {
	rt.Suite
}

func init() {
	rt.Register(&Disabled{}, rt.InArea("quic"), rt.NeedsManager(managers.Default))
}

// Test_GRPCTransport mirrors quic_disabled_test.go's Test_GRPCTransport:
// traffic reaches the cluster and status reports the plain "grpc"
// transport.
func (s *Disabled) Test_GRPCTransport() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("quic-disabled"))

	freshConnect(t, ctx, ns)
	rt.RoutedToCluster(t, wl.ServiceURL())

	st := fetchStatus(t, ctx, s.CLI())
	s.Equal("grpc", st.RootDaemon.TunnelTransport)
}

// Test_InterceptRoundTrips mirrors quic_disabled_test.go's
// Test_InterceptStillWorks: a regular intercept round-trips end to end
// while status keeps reporting the plain "grpc" transport.
func (s *Disabled) Test_InterceptRoundTrips() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("quic-disabled-intercept"))
	ls := s.LocalEcho()

	conn := freshConnect(t, ctx, ns)
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	st := fetchStatus(t, ctx, s.CLI())
	s.Equal("grpc", st.RootDaemon.TunnelTransport)
}

package quic

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// Enabled proves the quic transport takes over the manager-bound tunnel:
// with managers.QuicNodePort() installed (a NodePort Service whose
// advertised endpoint the manager self-discovers, see the catalog's doc
// comment), "telepresence status" settles on the quic transport and a
// regular intercept round-trips over it. Mirrors the core of quic_test.go's
// Test_VPNOnlyTransport/Test_TrafficAgentCoexistence.
type Enabled struct {
	rt.Suite
}

func init() {
	rt.Register(&Enabled{}, rt.InArea("quic"), rt.NeedsManager(managers.QuicNodePort()))
}

func (s *Enabled) Test_QuicTransport() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	tp := s.CLI()
	wl := s.Workload(workloads.Echo("quic-enabled"))
	ls := s.LocalEcho()

	conn := awaitTransportPrefix(t, ctx, tp, ns, freshConnect(t, ctx, ns), quicPrefix)

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)

	// The client-to-agent attachment itself rides quic too (agents get the
	// same QUIC listener plumbing as the manager-bound tunnel).
	awaitAgentTransport(t, ctx, tp, wl.Name, "quic", quicStatusTimeout)

	st := fetchStatus(t, ctx, tp)
	s.True(strings.HasPrefix(st.RootDaemon.TunnelTransport, quicPrefix),
		"status should still report the quic transport with an active intercept, got %q",
		st.RootDaemon.TunnelTransport)
}

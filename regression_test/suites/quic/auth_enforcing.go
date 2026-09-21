package quic

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AuthEnforcingTransport proves a QUIC tunnel is accepted under
// security.authentication.mode=enforcing, where the session credential the
// QUIC listener attaches to the stream's context stands in for the missing
// principal on every tunnel RPC.
type AuthEnforcingTransport struct {
	rt.Suite
}

func init() {
	rt.Register(&AuthEnforcingTransport{}, rt.InArea("quic"), rt.NeedsManager(managers.QuicNodePortAuthEnforcing()))
}

// Test_QuicTunnelAcceptedUnderEnforcing connects until the quic transport is
// active against an enforcing-mode manager, then intercepts a workload and
// routes traffic through it, so a fallback to grpc -- whose tunnel RPCs
// would be rejected as unowned -- fails the test.
func (s *AuthEnforcingTransport) Test_QuicTunnelAcceptedUnderEnforcing() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	tp := s.CLI()
	wl := s.Workload(workloads.Echo("quic-auth-enforcing"))
	ls := s.LocalEcho()

	conn := awaitTransportPrefix(t, ctx, tp, ns, freshConnect(t, ctx, ns))

	st := fetchStatus(t, ctx, tp)
	s.True(strings.HasPrefix(st.RootDaemon.TunnelTransport, quicPrefix),
		"tunnel_transport should report the quic transport under enforcing mode, got %q",
		st.RootDaemon.TunnelTransport)

	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
}

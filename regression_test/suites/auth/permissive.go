package auth

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AuthPermissive proves that security.authentication.mode=permissive leaves
// the standard connect/intercept flow unaffected, and never rejects a call
// for lacking a bearer token.
type AuthPermissive struct {
	rt.Suite
}

func init() {
	rt.Register(&AuthPermissive{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthPermissive()))
}

// Test_ConnectAndInterceptWork drives a plain connect -> intercept -> detach
// cycle against a permissive-mode manager, under the framework's standard
// test-developer identity.
func (s *AuthPermissive) Test_ConnectAndInterceptWork() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := switchManagerSpec(t, ctx, managers.AuthPermissive(), ns)

	wl := s.Workload(workloads.Echo("auth-permissive"))
	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
}

// Test_StreamLogsRefusesUnauthenticatedCaller asserts that a tokenless
// session still gets Unauthenticated from StreamLogs on the first Recv when
// the call itself carries no token.
func (s *AuthPermissive) Test_StreamLogsRefusesUnauthenticatedCaller() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	si, err := mc.ArriveAsClient(ctx, &manager.ClientInfo{
		Name:      "rtest-auth-permissive-streamlogs",
		Namespace: ns,
		InstallId: "rtest-auth-permissive-streamlogs",
		Product:   "telepresence",
		Version:   "v" + r.Version().String(),
	})
	s.Require().NoError(err)
	defer func() { _, _ = mc.Depart(ctx, si) }()

	stream, err := mc.StreamLogs(ctx, &manager.StreamLogsRequest{Session: si, TrafficManager: true})
	s.Require().NoError(err)
	_, err = stream.Recv()
	s.Require().Error(err)
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.Unauthenticated, st.Code())
}

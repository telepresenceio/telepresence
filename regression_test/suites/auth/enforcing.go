package auth

import (
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// AuthEnforcing proves security.authentication.mode=enforcing: the
// clientRbac-granted test-developer identity connects and intercepts as
// usual, an identity with no RBAC bindings at all is denied before it ever
// reaches a session, and the manager's raw gRPC surface itself requires and
// binds a verified bearer-token identity per session.
//
// Two scenarios are intentionally out of scope: a cert-only client
// authenticating over the manager's dedicated x509 auth port, and the
// security.authentication.x509.enabled=false rejection of that same client.
// Both need client-certificate kubeconfig plumbing, which this framework has
// not ported ("x509 client-cert plumbing not ported").
type AuthEnforcing struct {
	rt.Suite
}

func init() {
	rt.Register(&AuthEnforcing{}, rt.InArea("auth"), rt.NeedsManager(managers.AuthEnforcing()))
}

// Test_GrantedIdentityConnectsAndIntercepts drives a plain connect ->
// intercept -> detach cycle under the clientRbac-granted test-developer
// identity (the framework's default --as connection identity) against an
// enforcing-mode manager.
func (s *AuthEnforcing) Test_GrantedIdentityConnectsAndIntercepts() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := switchManagerSpec(t, ctx, managers.AuthEnforcing(), ns)

	wl := s.Workload(workloads.Echo("auth-enforcing"))
	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
}

// Test_UnauthorizedIdentityDeniedAtConnect creates a ServiceAccount in the
// manager namespace with no bindings beyond the chart's own defaults (in
// particular, none of clientRbac's traffic-manager-connect Role, which
// grants pods/portforward create in that namespace) and connects --as that
// identity. The Kubernetes API server itself refuses the resulting
// port-forward to the traffic-manager pod, so the denial happens before the
// client ever reaches a manager RPC -- independent of the manager's own
// authentication mode, but grouped here since it is the identity scenario
// this area's enforcing suite exists to cover.
func (s *AuthEnforcing) Test_UnauthorizedIdentityDeniedAtConnect() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createNoGrantsServiceAccount(t, ctx, r, "rtest-auth-noperms-connect")

	freeDefaultConnection(t, ns)
	quitDefensively(t, r, ctx, "AuthEnforcing:UnauthorizedIdentityDeniedAtConnect")

	ok, stderr := probeConnectAs(t, r, ctx, ns, identity(name))
	s.False(ok, "connect with an unauthorized identity should be refused")
	s.Contains(strings.ToLower(stderr), "forbidden")
}

// Test_SessionRequiresAndBindsVerifiedIdentity drives the manager's raw gRPC
// surface directly via rt.ManagerClient, without going through a `telepresence
// connect` session at all: an unauthenticated ArriveAsClient is rejected
// outright (enforcing mode requires a bearer token on every call), a bearer
// token for the clientRbac-granted identity establishes a session, and a
// later call presenting a different identity's token against that same
// session is rejected as bound to another identity. Uses bearer tokens only
// (`kubectl create token`), with no x509 client-certificate plumbing.
func (s *AuthEnforcing) Test_SessionRequiresAndBindsVerifiedIdentity() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	s.Require().NoError(err)
	defer closeFn()

	// No token at all: enforcing mode rejects the call outright, before it
	// ever reaches session bookkeeping.
	_, err = mc.ArriveAsClient(ctx, &manager.ClientInfo{
		Name:      "rtest-auth-enforcing-unauthenticated",
		Namespace: ns,
		InstallId: "rtest-auth-enforcing-unauthenticated",
		Product:   "telepresence",
		Version:   "v" + s.R().Version().String(),
	})
	s.Error(err, "an unauthenticated ArriveAsClient should be rejected under enforcing mode")
	if st, ok := status.FromError(err); ok {
		s.Equal(codes.Unauthenticated, st.Code())
	}

	grantedTok := kubectlCreateToken(t, ctx, r, managers.ManagerNamespace, managers.TestServiceAccount)
	grantedCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+grantedTok)

	si, err := mc.ArriveAsClient(grantedCtx, &manager.ClientInfo{
		Name:      "rtest-auth-enforcing-granted",
		Namespace: ns,
		InstallId: "rtest-auth-enforcing-granted",
		Product:   "telepresence",
		Version:   "v" + s.R().Version().String(),
	})
	s.Require().NoError(err, "ArriveAsClient with the granted identity's token should succeed")
	defer func() { _, _ = mc.Depart(grantedCtx, si) }()

	otherName := createNoGrantsServiceAccount(t, ctx, r, "rtest-auth-noperms-session")
	otherTok := kubectlCreateToken(t, ctx, r, managers.ManagerNamespace, otherName)
	otherCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+otherTok)

	_, err = mc.Remain(otherCtx, &manager.RemainRequest{Session: si})
	s.Error(err, "Remain with a different identity's token should be rejected")
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.PermissionDenied, st.Code())
	s.Contains(st.Message(), "bound to another identity")
}

package integration_test

import (
	"bytes"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
)

// managerAuthSuite exercises the traffic-manager's security.authentication.mode
// enforcement. It owns a dedicated manager+app namespace pair installed with
// mode=enforcing, separate from the suite-shared, permissive manager that the
// rest of the harness uses.
type managerAuthSuite struct {
	itest.Suite
	itest.SingleService
	appNS, mgrNS string
}

func (s *managerAuthSuite) SuiteName() string {
	return "ManagerAuth"
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) itest.TestingSuite {
		s := &managerAuthSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
		suffix := itest.GetGlobalHarness(h.HarnessContext()).Suffix()
		s.appNS, s.mgrNS = itest.AppAndMgrNSName(suffix + "-mgrauth")
		return s
	})
}

func (s *managerAuthSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	rq := s.Require()

	itest.CreateNamespaces(ctx, s.appNS, s.mgrNS)
	itest.ApplyEchoService(ctx, s.ServiceName(), s.appNS, 80)

	// telepresence-test-developer: gets the standard connect+intercept
	// clientRbac grants below, once the manager is installed.
	rq.NoError(itest.Kubectl(ctx, s.mgrNS, "apply", "-f", filepath.Join("testdata", "k8s", "client_sa.yaml")))

	// manager-auth-connect-only: can reach the manager but has no RBAC
	// whatsoever in the app namespace.
	yml, err := itest.ReadTemplate(ctx, filepath.Join("testdata", "k8s", "manager_auth_connect_only.goyaml"), map[string]string{
		"ManagerNamespace": s.mgrNS,
	})
	rq.NoError(err)
	rq.NoError(itest.Kubectl(dos.WithStdin(ctx, bytes.NewReader(yml)), s.mgrNS, "apply", "-f", "-"))

	hctx := itest.WithNamespaces(ctx, &itest.Namespaces{
		Namespace: s.mgrNS,
		Selector:  labels.SelectorFromNames(s.appNS),
	})
	s.TelepresenceHelmInstallOK(hctx, false, "--set", "security.authentication.mode=enforcing")
}

func (s *managerAuthSuite) TearDownSuite() {
	ctx := s.Context()
	s.UninstallTrafficManager(ctx, s.mgrNS)
	itest.DeleteNamespaces(ctx, s.appNS, s.mgrNS)
	s.TelepresenceConnect(ctx)
}

func (s *managerAuthSuite) SetupTest() {
	// The suite harness may be connected to the shared manager; every test
	// here drives its own connection, so start from a quiesced daemon.
	itest.TelepresenceQuitOk(s.Context())
}

func (s *managerAuthSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

// Test_EnforcingAcceptsCertOnlyClient connects with the plain, cert-only
// admin context against the enforcing manager. The manager's x509 auth
// listener, enabled by default under enforcing mode, authenticates the
// kubeconfig's client certificate, so the connect succeeds and the derived
// identity passes the intercept authorization.
func (s *managerAuthSuite) Test_EnforcingAcceptsCertOnlyClient() {
	// The suite context carries an impersonation user; this test needs the
	// plain cert-only admin context.
	ctx := itest.WithUser(s.Context(), "default")
	stdout := itest.TelepresenceOk(ctx, "connect", "--namespace", s.appNS, "--manager-namespace", s.mgrNS)
	s.Contains(stdout, "Connected to context")

	defer itest.TelepresenceOk(ctx, "detach", s.ServiceName())
	stdout = itest.TelepresenceOk(ctx, "intercept", "--mount", "false", s.ServiceName(), "--port", "9090")
	s.Contains(stdout, "Using Deployment "+s.ServiceName())
	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.appNS, "--intercepts")
	s.Contains(stdout, s.ServiceName()+": intercepted")
}

// Test_EnforcingRejectsCertOnlyClientWhenX509Disabled upgrades the manager
// with security.authentication.x509.enabled=false and expects the cert-only
// admin context to be refused before ever reaching the manager's RPCs, since
// the kubeconfig can produce neither a bearer token nor an x509 path.
func (s *managerAuthSuite) Test_EnforcingRejectsCertOnlyClientWhenX509Disabled() {
	ctx := itest.WithUser(s.Context(), "default")
	hctx := itest.WithNamespaces(ctx, &itest.Namespaces{
		Namespace: s.mgrNS,
		Selector:  labels.SelectorFromNames(s.appNS),
	})
	s.TelepresenceHelmInstallOK(hctx, true,
		"--set", "security.authentication.mode=enforcing",
		"--set", "security.authentication.x509.enabled=false")
	defer s.TelepresenceHelmInstallOK(hctx, true, "--set", "security.authentication.mode=enforcing")

	_, stderr, err := itest.Telepresence(ctx, "connect", "--namespace", s.appNS, "--manager-namespace", s.mgrNS)
	s.Error(err)
	s.Contains(stderr, "requires an authenticated client")
	s.Contains(stderr, "security.authentication.x509.enabled")
}

// Test_EnforcingAcceptsAuthenticatedClientAndIntercepts connects with a
// ServiceAccount token for telepresence-test-developer, which -- once the
// manager is installed with clientRbac.subjects pointing at it -- holds both
// the connect Role in the manager namespace and the intercept Role in the
// app namespace. Both connect and intercept are expected to succeed.
func (s *managerAuthSuite) Test_EnforcingAcceptsAuthenticatedClientAndIntercepts() {
	// The token, not the suite's impersonation user, must be the identity.
	ctx := itest.WithUser(s.Context(), "default")
	rq := s.Require()

	tok, err := itest.KubectlOut(ctx, s.mgrNS, "create", "token", itest.TestUser)
	rq.NoError(err)
	tok = strings.TrimSpace(tok)

	stdout := itest.TelepresenceOk(ctx, "connect", "--namespace", s.appNS, "--manager-namespace", s.mgrNS, "--token", tok)
	s.Contains(stdout, "Connected to context")

	defer itest.TelepresenceOk(ctx, "detach", s.ServiceName())
	stdout = itest.TelepresenceOk(ctx, "intercept", "--mount", "false", s.ServiceName(), "--port", "9090")
	s.Contains(stdout, "Using Deployment "+s.ServiceName())
	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.appNS, "--intercepts")
	s.Contains(stdout, s.ServiceName()+": intercepted")
}

// Test_EnforcingDeniesUnauthorizedIntercept connects with a token whose RBAC
// covers only the manager namespace (enough to port-forward to the manager
// and complete the auth handshake), but grants nothing in the app namespace.
// The manager's SubjectAccessReview-based intercept authorization is expected
// to deny the intercept.
func (s *managerAuthSuite) Test_EnforcingDeniesUnauthorizedIntercept() {
	// The token, not the suite's impersonation user, must be the identity.
	ctx := itest.WithUser(s.Context(), "default")
	rq := s.Require()

	tok, err := itest.KubectlOut(ctx, s.mgrNS, "create", "token", "manager-auth-connect-only")
	rq.NoError(err)
	tok = strings.TrimSpace(tok)

	stdout := itest.TelepresenceOk(ctx, "connect", "--namespace", s.appNS, "--manager-namespace", s.mgrNS, "--token", tok)
	s.Contains(stdout, "Connected to context")

	_, stderr, err := itest.Telepresence(ctx, "intercept", "--mount", "false", s.ServiceName(), "--port", "9090")
	s.Error(err)
	if !strings.Contains(stderr, "not permitted to create pods/portforward") {
		s.Contains(err.Error(), "not permitted to create pods/portforward")
	}
}

// Test_SessionBoundToAuthenticatedIdentity establishes a raw client session
// against the suite-shared (permissive) manager using a bearer token, then
// calls Remain on that session without any token. Because the session was
// created with a verified identity, the tokenless call must be rejected --
// this only holds in permissive mode, since an enforcing manager would
// already reject the tokenless call for lacking a token at all.
func (s *managerAuthSuite) Test_SessionBoundToAuthenticatedIdentity() {
	ctx := s.Context()
	rq := s.Require()

	k8sCluster, err := s.GetK8SCluster(ctx, "", s.ManagerNamespace())
	rq.NoError(err)

	conn, _, _, err := k8sCluster.ConnectToManager(ctx, s.ManagerNamespace())
	rq.NoError(err)
	defer conn.Close()

	tok, err := itest.KubectlOut(ctx, s.ManagerNamespace(), "create", "token", itest.TestUser)
	rq.NoError(err)
	tok = strings.TrimSpace(tok)

	mc := manager.NewManagerClient(conn)
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)

	si, err := mc.ArriveAsClient(authCtx, &manager.ClientInfo{
		Name:      "manager-auth-test",
		Namespace: s.AppNamespace(),
		InstallId: "manager-auth-test-install",
		Product:   "telepresence",
		Version:   client.Version(),
	})
	rq.NoError(err)
	defer func() {
		_, _ = mc.Depart(authCtx, si)
	}()

	_, err = mc.Remain(ctx, &manager.RemainRequest{Session: si})
	s.Error(err)
	st, ok := status.FromError(err)
	rq.True(ok)
	s.Equal(codes.PermissionDenied, st.Code())
	s.Contains(st.Message(), "bound to another identity")
}

package integration_test

import (
	"strings"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/labels"
)

// compatAuthSuite validates the authentication compatibility contract for
// clients that predate manager authentication (< 2.31.0) and never send a
// bearer token: such a client works unchanged against a manager running the
// default permissive mode, and is rejected with a clear error by an
// enforcing one.
//
// The suite is skipped unless the run is driven by such a client:
//
//	DEV_CLIENT_VERSION=2.30.1 make check-integration TEST_SUITE='^CompatAuth$'
//
// The manager under test is always the version being built; the harness uses
// the built binary for helm operations, so the manager is installed from the
// current chart even though connects are driven by the old client.
type compatAuthSuite struct {
	itest.Suite
	itest.SingleService
	appNS, mgrNS string
	svc          string
	installed    bool
}

func (s *compatAuthSuite) SuiteName() string {
	return "CompatAuth"
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) itest.TestingSuite {
		s := &compatAuthSuite{Suite: itest.Suite{Harness: h}, SingleService: h, svc: "echo"}
		suffix := itest.GetGlobalHarness(h.HarnessContext()).Suffix()
		s.appNS, s.mgrNS = itest.AppAndMgrNSName(suffix + "-compat")
		return s
	})
}

func (s *compatAuthSuite) SetupSuite() {
	s.Suite.SetupSuite()
	if s.ClientIsVersion(">2.30.x") {
		s.T().Skip("CompatAuth requires DEV_CLIENT_VERSION pointing at a client that predates manager authentication (< 2.31.0)")
	}
	ctx := s.Context()

	itest.CreateNamespaces(ctx, s.appNS, s.mgrNS)
	itest.ApplyEchoService(ctx, s.svc, s.appNS, 80)

	// The default mode: no security.authentication settings at all.
	hctx := itest.WithNamespaces(ctx, &itest.Namespaces{
		Namespace: s.mgrNS,
		Selector:  labels.SelectorFromNames(s.appNS),
	})
	s.TelepresenceHelmInstallOK(hctx, false)
	s.installed = true
}

func (s *compatAuthSuite) TearDownSuite() {
	if !s.installed {
		return
	}
	ctx := s.Context()
	s.UninstallTrafficManager(ctx, s.mgrNS)
	itest.DeleteNamespaces(ctx, s.appNS, s.mgrNS)
	s.TelepresenceConnect(ctx)
}

func (s *compatAuthSuite) SetupTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *compatAuthSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

// Test_PermissiveAcceptsLegacyClient connects the pre-authentication client
// to a manager running the default permissive mode. The client sends no
// token, and permissive mode must accept it: this is the staged-rollout
// guarantee that upgrading the traffic-manager never breaks an existing
// installation.
func (s *compatAuthSuite) Test_PermissiveAcceptsLegacyClient() {
	ctx := itest.WithUser(s.Context(), "default")
	stdout := itest.TelepresenceOk(ctx, "connect", "--namespace", s.appNS, "--manager-namespace", s.mgrNS)
	s.Contains(stdout, "Connected to context")
	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.appNS)
	s.Contains(stdout, s.svc)
}

// Test_EnforcingRejectsLegacyClient upgrades the manager to enforcing mode
// and expects the pre-authentication client, which cannot send a token, to
// be rejected by the manager with a message that explains why.
func (s *compatAuthSuite) Test_EnforcingRejectsLegacyClient() {
	ctx := itest.WithUser(s.Context(), "default")
	hctx := itest.WithNamespaces(ctx, &itest.Namespaces{
		Namespace: s.mgrNS,
		Selector:  labels.SelectorFromNames(s.appNS),
	})
	s.TelepresenceHelmInstallOK(hctx, true, "--set", "security.authentication.mode=enforcing")
	defer s.TelepresenceHelmInstallOK(hctx, true, "--set", "security.authentication.mode=permissive")

	_, stderr, err := itest.Telepresence(ctx, "connect", "--namespace", s.appNS, "--manager-namespace", s.mgrNS)
	s.Error(err)
	if !strings.Contains(stderr, "requires an authenticated caller") {
		s.Contains(err.Error(), "requires an authenticated caller")
	}
}

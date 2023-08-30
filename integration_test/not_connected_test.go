package integration_test

import (
	"fmt"
	"regexp"
	"runtime"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type notConnectedSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *notConnectedSuite) SuiteName() string {
	return "NotConnected"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &notConnectedSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *notConnectedSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *notConnectedSuite) Test_ConnectWithCommand() {
	ctx := s.Context()
	stdout := s.TelepresenceConnect(ctx, "--", s.Executable(), "status")
	s.Contains(stdout, "Connected to context")
	s.Contains(stdout, "Kubernetes context:")
}

func (s *notConnectedSuite) Test_InvalidKubeconfig() {
	ctx := s.Context()
	path := "/dev/null"
	if runtime.GOOS == "windows" {
		path = "C:\\NUL"
	}
	badEnvCtx := itest.WithEnv(ctx, map[string]string{"KUBECONFIG": path})
	_, stderr, err := itest.Telepresence(badEnvCtx, "connect")
	s.Contains(stderr, "kubeconfig has no context definition")
	s.Error(err)
}

func (s *notConnectedSuite) Test_NonExistentContext() {
	ctx := s.Context()
	_, stderr, err := itest.Telepresence(ctx, "connect", "--context", "not-likely-to-exist")
	s.Error(err)
	s.Contains(stderr, "context was not found")
}

func (s *notConnectedSuite) Test_ConnectingToOtherNamespace() {
	ctx := s.Context()

	suffix := itest.GetGlobalHarness(s.HarnessContext()).Suffix()
	appSpace2, mgrSpace2 := itest.AppAndMgrNSName(suffix + "-2")
	itest.CreateNamespaces(ctx, appSpace2, mgrSpace2)
	defer itest.DeleteNamespaces(ctx, appSpace2, mgrSpace2)

	s.Run("Installs Successfully", func() {
		ctx := itest.WithNamespaces(s.Context(), &itest.Namespaces{
			Namespace:         mgrSpace2,
			ManagedNamespaces: []string{appSpace2},
		})
		s.NoError(s.TelepresenceHelmInstall(ctx, false))
	})

	s.Run("Can be connected to with --manager-namespace-flag", func() {
		ctx := s.Context()
		itest.TelepresenceQuitOk(ctx)

		// Set the config to some nonsense to verify that the flag wins
		ctx = itest.WithConfig(ctx, func(cfg client.Config) {
			cfg.Cluster().DefaultManagerNamespace = "daffy-duck"
		})
		ctx = itest.WithUser(ctx, mgrSpace2+":"+itest.TestUser)
		stdout := itest.TelepresenceOk(ctx, "connect", "--namespace", appSpace2, "--manager-namespace="+mgrSpace2)
		s.Contains(stdout, "Connected to context")
		stdout = itest.TelepresenceOk(ctx, "status")
		s.Regexp(`Manager namespace\s+: `+mgrSpace2, stdout)
	})

	s.Run("Can be connected to with defaultManagerNamespace config", func() {
		ctx := s.Context()
		itest.TelepresenceQuitOk(ctx)
		ctx = itest.WithConfig(ctx, func(cfg client.Config) {
			cfg.Cluster().DefaultManagerNamespace = mgrSpace2
		})
		stdout := itest.TelepresenceOk(itest.WithUser(ctx, "default"), "connect")
		s.Contains(stdout, "Connected to context")
		stdout = itest.TelepresenceOk(ctx, "status")
		s.Regexp(`Manager namespace\s+: `+mgrSpace2, stdout)
	})

	s.Run("Uninstalls Successfully", func() {
		s.UninstallTrafficManager(s.Context(), mgrSpace2)
	})
}

func (s *notConnectedSuite) Test_ReportsNotConnected() {
	ctx := s.Context()
	itest.TelepresenceOk(itest.WithUser(ctx, "default"), "connect")
	itest.TelepresenceDisconnectOk(ctx)
	stdout := itest.TelepresenceOk(ctx, "version")
	rxVer := regexp.QuoteMeta(s.TelepresenceVersion())
	s.Regexp(fmt.Sprintf(`Client\s*: %s`, rxVer), stdout)
	s.Regexp(fmt.Sprintf(`Root Daemon\s*: %s`, rxVer), stdout)
	s.Regexp(fmt.Sprintf(`User Daemon\s*: %s`, rxVer), stdout)
	s.Regexp(`Traffic Manager\s*: not connected`, stdout)
}

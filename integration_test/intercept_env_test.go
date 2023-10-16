package integration

import (
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type interceptEnvSuite struct {
	itest.Suite
	itest.NamespacePair
}

func (s *interceptEnvSuite) SuiteName() string {
	return "InterceptEnv"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &interceptEnvSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *interceptEnvSuite) Test_ExcludeVariables() {
	// given
	ctx := s.Context()
	err := s.TelepresenceHelmInstall(ctx, false, "--set", "intercept.environment.excluded={DATABASE_HOST,DATABASE_PASSWORD}")
	s.Require().NoError(err)
	defer s.UninstallTrafficManager(ctx, s.ManagerNamespace())

	s.ApplyApp(ctx, "echo_with_env", "deploy/echo-easy")
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")

	helloEnv := filepath.Join(s.T().TempDir(), "echo.env")

	// when
	s.TelepresenceConnect(ctx)
	itest.TelepresenceOk(ctx, "intercept", "echo-easy", "--env-file", helloEnv)

	// then
	file, err := os.ReadFile(helloEnv)
	s.Require().NoError(err)

	s.NotContains(string(file), "DATABASE_HOST")
	s.NotContains(string(file), "DATABASE_PASSWORD")
	s.Contains(string(file), "TEST=DATA")
	s.Contains(string(file), "INTERCEPT=ENV")
}

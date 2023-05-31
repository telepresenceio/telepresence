package integration

import (
	"os"

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
	itest.AddTrafficManagerSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &interceptEnvSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *interceptEnvSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *interceptEnvSuite) Test_ExcludeVariables() {
	// given
	ctx := s.Context()
	err := s.TelepresenceHelmInstall(ctx, true, "--set", "intercept.environment.excluded={DATABASE_HOST,DATABASE_PASSWORD}")
	s.Assert().NoError(err)
	s.ApplyApp(ctx, "echo_with_env", "deploy/echo-easy")

	defer s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
	defer os.RemoveAll("echo.env") //nolint:errcheck // dont need to catch the err

	// when
	itest.TelepresenceOk(ctx, "intercept", "echo-easy", "--namespace", s.AppNamespace(), "--env-file", "echo.env")

	// then
	file, err := os.ReadFile("echo.env")
	s.Assert().NoError(err)

	s.Assert().NotContains(string(file), "DATABASE_HOST")
	s.Assert().NotContains(string(file), "DATABASE_PASSWORD")
	s.Assert().Contains(string(file), "TEST=DATA")
	s.Assert().Contains(string(file), "INTERCEPT=ENV")
}

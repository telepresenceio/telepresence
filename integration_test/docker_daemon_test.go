package integration_test

import (
	"context"
	"encoding/json"
	goRuntime "runtime"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type dockerDaemonSuite struct {
	itest.Suite
	itest.NamespacePair
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.NamespacePair) suite.TestingSuite {
		return &dockerDaemonSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *dockerDaemonSuite) Context() context.Context {
	ctx := itest.WithConfig(s.Suite.Context(), func(cfg *client.Config) {
		cfg.Intercept.UseFtp = false
	})
	return itest.WithUseDocker(ctx, true)
}

func (s *dockerDaemonSuite) SetupSuite() {
	if s.IsCI() && goRuntime.GOOS != "linux" {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
		return
	}
	s.Suite.SetupSuite()
}

func (s *dockerDaemonSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *dockerDaemonSuite) Test_DockerDaemon_status() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())

	jsOut := itest.TelepresenceOk(ctx, "status", "--output", "json")

	require := s.Require()
	var statusMap map[string]any
	require.NoError(json.Unmarshal([]byte(jsOut), &statusMap))
	ud, ok := statusMap["user_daemon"]
	s.True(ok)
	udm, ok := ud.(map[string]any)
	s.True(ok)
	s.Equal(udm["running"], true)
	s.Equal(udm["name"], "OSS Daemon in container")
	s.Equal(udm["status"], "Connected")
}

func (s *dockerDaemonSuite) Test_DockerDaemon_hostDaemonConflict() {
	ctx := s.Context()
	defer itest.TelepresenceQuitOk(itest.WithUseDocker(ctx, false))
	itest.TelepresenceOk(itest.WithUseDocker(ctx, false), "connect", "--manager-namespace", s.ManagerNamespace())

	_, stdErr, err := itest.Telepresence(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
	s.Error(err)
	s.Contains(stdErr, "option --docker cannot be used as long as a daemon is running on the host")
}

func (s *dockerDaemonSuite) Test_DockerDaemon_daemonHostNotConflict() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
	itest.TelepresenceOk(itest.WithUseDocker(ctx, false), "connect", "--manager-namespace", s.ManagerNamespace())
}

package integration_test

import (
	"context"
	"encoding/json"
	goRuntime "runtime"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type dockerDaemonSuite struct {
	itest.Suite
	itest.NamespacePair
	ctx context.Context
}

func (s *dockerDaemonSuite) SuiteName() string {
	return "DockerDaemon"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &dockerDaemonSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *dockerDaemonSuite) SetupSuite() {
	if s.IsCI() && goRuntime.GOOS != "linux" {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
		return
	}
	s.Suite.SetupSuite()
	ctx := itest.WithConfig(s.HarnessContext(), func(cfg client.Config) {
		cfg.Intercept().UseFtp = false
	})
	s.ctx = itest.WithUseDocker(ctx, true)
}

func (s *dockerDaemonSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *dockerDaemonSuite) Context() context.Context {
	return itest.WithT(s.ctx, s.T())
}

func (s *dockerDaemonSuite) Test_DockerDaemon_status() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx)

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
	s.TelepresenceConnect(itest.WithUseDocker(ctx, false))

	_, stdErr, err := itest.Telepresence(ctx, "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
	s.Error(err)
	s.Contains(stdErr, "option --docker cannot be used as long as a daemon is running on the host")
}

func (s *dockerDaemonSuite) Test_DockerDaemon_daemonHostNotConflict() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	itest.TelepresenceOk(itest.WithUseDocker(ctx, false), "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
}

package integration_test

import (
	"context"
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
	s.ctx = itest.WithConfig(s.HarnessContext(), func(cfg client.Config) {
		cfg.Intercept().UseFtp = false
	})
}

func (s *dockerDaemonSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *dockerDaemonSuite) Context() context.Context {
	return itest.WithT(s.ctx, s.T())
}

func (s *dockerDaemonSuite) Test_DockerDaemon_status() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--docker")

	status := itest.TelepresenceStatusOk(ctx)
	ud := status.UserDaemon
	s.True(ud.Running)
	s.Equal(ud.Name, "OSS Daemon in container")
	s.Equal(ud.Status, "Connected")
}

func (s *dockerDaemonSuite) Test_DockerDaemon_hostDaemonConflict() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	_, stdErr, err := itest.Telepresence(ctx, "connect", "--docker", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
	s.Error(err)
	s.Contains(stdErr, "option --docker cannot be used as long as a daemon is running on the host")
}

func (s *dockerDaemonSuite) Test_DockerDaemon_daemonHostNotConflict() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--docker")
	s.TelepresenceConnect(ctx)
}

package integration_test

import (
	"context"
	"encoding/json"
	"path/filepath"
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
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) suite.TestingSuite {
		return &dockerDaemonSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *dockerDaemonSuite) Context() context.Context {
	return itest.WithConfig(s.Suite.Context(), func(cfg *client.Config) {
		cfg.Intercept.UseFtp = false
	})
}

func (s *dockerDaemonSuite) SetupSuite() {
	if s.IsCI() && goRuntime.GOOS != "linux" {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
		return
	}
	s.Suite.SetupSuite()
	args := append([]string{"helm", "install", "--docker", "-n", s.ManagerNamespace()}, s.GetValuesForHelm(nil, false, s.ManagerNamespace(), s.AppNamespace())...)
	args = append(args, "-f", filepath.Join("testdata", "namespaced-values.yaml"))

	ctx := s.Context()
	ctx = itest.WithWorkingDir(itest.WithUser(ctx, "default"), filepath.Join(itest.GetOSSRoot(ctx), "integration_test"))
	itest.TelepresenceOk(ctx, args...)
}

func (s *dockerDaemonSuite) TearDownSuite() {
	itest.TelepresenceOk(itest.WithUser(s.Context(), "default"), "helm", "uninstall", "-n", s.ManagerNamespace(), "--docker")
}

func (s *dockerDaemonSuite) Test_DockerDaemon_status() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace(), "--docker")
	defer itest.TelepresenceQuitOk(ctx)

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
	defer itest.TelepresenceQuitOk(ctx)
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())

	_, stdErr, err := itest.Telepresence(ctx, "connect", "--manager-namespace", s.ManagerNamespace(), "--docker")
	s.Error(err)
	s.Contains(stdErr, "option --docker cannot be used as long as a daemon is running on the host")
}

func (s *dockerDaemonSuite) Test_DockerDaemon_daemonHostNotConflict() {
	ctx := s.Context()
	defer itest.TelepresenceQuitOk(ctx)
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace(), "--docker")
	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
}

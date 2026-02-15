package integration_test

import (
	"context"
	"os"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type composeSuite struct {
	itest.Suite
	itest.TrafficManager
	ctx context.Context
}

func (s *composeSuite) SuiteName() string {
	return "Compose"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &composeSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (s *composeSuite) SetupSuite() {
	if s.IsCI() && !(goRuntime.GOOS == "linux" && goRuntime.GOARCH == "amd64") {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
		return
	}
	s.Suite.SetupSuite()
	s.ctx = itest.WithConfig(s.HarnessContext(), func(cfg client.Config) {
		cfg.Intercept().UseFtp = false
	})
}

func (s *composeSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *composeSuite) Context() context.Context {
	return itest.WithT(s.ctx, s.T())
}

func (s *composeSuite) Test_ComposeDNS() {
	ctx := s.Context()
	require := s.Require()

	const svc = "echo-easy"
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	const svc2 = "echo-other"
	s.ApplyEchoService(ctx, svc2, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc2)

	// Write a docker-compose.yml that replaces echo-other and sleeps.
	composeDir := itest.TempDir(ctx)
	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	ns := s.AppNamespace()
	composeContent := strings.Join([]string{
		"x-tele:",
		"  connections:",
		"    - namespace: " + ns,
		"      manager-namespace: " + s.ManagerNamespace(),
		"services:",
		"  " + svc2 + ":",
		"    x-tele:",
		"      type: replace",
		"    image: busybox",
		"    command: sleep infinity",
	}, "\n")
	require.NoError(os.WriteFile(composeFile, []byte(composeContent), 0o644))

	_, _, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "up", "-d")
	require.NoError(err, "compose up failed")
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "down")
	}()

	// Verify that cluster DNS resolves inside the compose container.
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "exec", svc2, "nslookup", svc)
		if err == nil && strings.Contains(so, "Address:") {
			return true
		}
		clog.Info(ctx, "nslookup", "sdtout", so, "stderr", se, "err", err)
		return false
	}, 30*time.Second, 3*time.Second, "nslookup of %s in compose container should succeed", svc)
}

package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type inactiveClientSuite struct {
	itest.Suite
	itest.NamespacePair

	// The name of the workload to intercept.
	svc string

	// The time a client can be inactive before it loses its right to blocks conflicting intercepts.
	inactiveBlockTimeout time.Duration

	// How often the client pings the manager to keep its right to block intercepts.
	pingInterval time.Duration
}

func (s *inactiveClientSuite) SuiteName() string {
	return "InactiveClient"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &inactiveClientSuite{
			Suite:                itest.Suite{Harness: h},
			NamespacePair:        h,
			svc:                  "echo-easy",
			inactiveBlockTimeout: 10 * time.Second,
			pingInterval:         2 * time.Second,
		}
	})
}

func (s *inactiveClientSuite) SetupSuite() {
	s.Suite.SetupSuite()

	// Default here is 10 minutes. We don't want to wait that long for the test to complete.
	s.TelepresenceHelmInstallOK(s.Context(), false, "--set", "logLevel=trace,intercept.inactiveBlockTimeout="+s.inactiveBlockTimeout.String())
	ctx := s.Context()

	s.ApplyApp(ctx, s.svc, "deploy/"+s.svc)
}

func (s *inactiveClientSuite) TearDownSuite() {
	ctx := s.Context()
	s.DeleteSvcAndWorkload(ctx, "deploy", s.svc)
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
}

func (s *inactiveClientSuite) AmendSuiteContext(ctx context.Context) context.Context {
	return itest.WithConfig(ctx, func(cfg client.Config) {
		cfg.Intercept().UseFtp = false

		// Ping is normally one minute, we need to be faster here.
		cfg.Grpc().PingInterval = s.pingInterval
	})
}

func (s *inactiveClientSuite) Test_ConflictOverrideInactive() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--docker", "--name", "conflict-one")
	defer func() {
		// Clean up the successful intercept
		itest.TelepresenceDisconnect(ctx, "--use", "conflict-one")
	}()

	require := s.Require()

	// This test verifies that real TCP port conflicts are still properly detected
	// when two intercepts try to use the same local port

	// Start first intercept using port 8080
	stdout1, stderr1, err1 := itest.Telepresence(ctx, "--use", "conflict-one", "intercept", "conflict-one",
		"--workload", s.svc,
		"--http-header", "x-user=adam",
		"--port", "8080:80",
		"--mount", "false")
	require.NoError(err1, "First intercept should succeed - stderr: %s", stderr1)
	require.Contains(stdout1, "Using Deployment")
	s.CapturePodLogs(ctx, s.svc, "traffic-agent", s.AppNamespace())

	// Create a second client.
	s.TelepresenceConnect(ctx, "--docker", "--name", "conflict-two")
	defer func() {
		// Clean up the successful intercept
		itest.TelepresenceDisconnect(ctx, "--use", "conflict-two")
	}()

	// Attempt second intercept using the same header. This should fail with a header conflict
	_, stderr2, err2 := itest.Telepresence(ctx, "--use", "conflict-two", "intercept", "conflict-two",
		"--workload", s.svc,
		"--http-header", "x-user=adam",
		"--port", "8081:80",
		"--mount", "false")

	// Should fail due to real TCP port conflict
	require.Error(err2, "Second intercept should fail due to header conflict")

	// Verify it's a header conflict
	s.Contains(stderr2, "header filters overlap")

	// Sleep until the first client have lost its right to block intercepts.
	time.Sleep(s.inactiveBlockTimeout + s.pingInterval)
	// Attempt the second intercept again. This should now succeed.
	_, _, err2 = itest.Telepresence(ctx, "--use", "conflict-two", "intercept", "conflict-two",
		"--workload", s.svc,
		"--http-header", "x-user=adam",
		"--port", "8081:80",
		"--mount", "false")
	s.NoError(err2, "Second intercept should succeed when the first client is sleeping")

	// The client that woke up should now see the intercept in an error state explaining the conflict.
	so, se, err := itest.Telepresence(ctx, "--use", "conflict-one", "list", "--intercepts")
	s.NoErrorf(err, "Failed to list intercepts - stderr: %s", se)
	if se != "" {
		clog.Error(ctx, se)
	}
	s.Regexp(regexp.MustCompile(`(?m)Intercept name: conflict-one\n.*AGENT_ERROR: conflict with intercept [\w-]+:conflict-two`), so)
	clog.Info(ctx, so)
}

func (s *inactiveClientSuite) Test_ConflictOverrideSleeping() {
	ctx := s.Context()

	s.TelepresenceConnect(ctx, "--docker", "--name", "conflict-one")
	defer func() {
		// Clean up the successful intercept
		itest.TelepresenceDisconnect(ctx, "--use", "conflict-one")
	}()

	require := s.Require()

	// This test verifies that real TCP port conflicts are still properly detected
	// when two intercepts try to use the same local port

	// Start first intercept using port 8080
	stdout1, stderr1, err1 := itest.Telepresence(ctx, "--use", "conflict-one", "intercept", "conflict-one",
		"--workload", s.svc,
		"--http-header", "x-user=adam",
		"--port", "8080:80",
		"--mount", "false")
	require.NoError(err1, "First intercept should succeed - stderr: %s", stderr1)
	require.Contains(stdout1, "Using Deployment")
	s.CapturePodLogs(ctx, s.svc, "traffic-agent", s.AppNamespace())

	// Create a second client.
	s.TelepresenceConnect(ctx, "--docker", "--name", "conflict-two")
	defer func() {
		// Clean up the successful intercept
		itest.TelepresenceDisconnect(ctx, "--use", "conflict-two")
	}()

	// Attempt second intercept using the same header. This should fail with a header conflict
	_, stderr2, err2 := itest.Telepresence(ctx, "--use", "conflict-two", "intercept", "conflict-two",
		"--workload", s.svc,
		"--http-header", "x-user=adam",
		"--port", "8081:80",
		"--mount", "false")

	// Should fail due to real TCP port conflict
	require.Error(err2, "Second intercept should fail due to header conflict")

	// Verify it's a header conflict
	s.Contains(stderr2, "header filters overlap")

	// Try again, but this time put the first client to sleep.
	s.withSleepingClient(ctx, "conflict-one", func(ctx context.Context) {
		// Sleep until the first client have lost its right to block intercepts.
		time.Sleep(s.inactiveBlockTimeout + s.pingInterval)
		// Attempt the second intercept again. This should now succeed.
		_, _, err2 = itest.Telepresence(ctx, "--use", "conflict-two", "intercept", "conflict-two",
			"--workload", s.svc,
			"--http-header", "x-user=adam",
			"--port", "8081:80",
			"--mount", "false")
		s.NoError(err2, "Second intercept should succeed when the first client is sleeping")
	})

	// The client that woke up should now see the intercept in an error state explaining the conflict.
	so, se, err := itest.Telepresence(ctx, "--use", "conflict-one", "list", "--intercepts")
	s.NoErrorf(err, "Failed to list intercepts - stderr: %s", se)
	if se != "" {
		clog.Error(ctx, se)
	}
	s.Regexp(regexp.MustCompile(`(?m)Intercept name: conflict-one\n.*AGENT_ERROR: conflict with intercept [\w-]+:conflict-two`), so)
	clog.Info(ctx, so)
}

func (s *inactiveClientSuite) withSleepingClient(ctx context.Context, clientName string, f func(ctx context.Context)) {
	// Put the client to sleep but keep its daemon info file alive. We don't want
	// other CLI commands to remove it when it goes stale.
	s.Require().NoError(itest.Run(ctx, "docker", "pause", clientName))
	infoFile := clientName + ".json"
	go func() {
		s.NoError(daemon.NewUserInfoLoader(ctx).KeepInfoAlive(infoFile))
	}()
	f(ctx)

	// Unpause and restore the info file timestamp (because unpause reverts the timestamp).
	fullInfoFile := filepath.Join(filelocation.AppUserCacheDir(ctx), "userd", infoFile)
	st, err := os.Stat(fullInfoFile)
	s.NoError(err)
	s.NoError(itest.Run(ctx, "docker", "unpause", clientName))
	s.NoError(os.Chtimes(fullInfoFile, time.Time{}, st.ModTime()))
}

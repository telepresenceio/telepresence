package integration_test

import (
	"context"
	"path/filepath"
	"regexp"
	goRuntime "runtime"
	"strings"
	"time"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
)

func (s *singleServiceSuite) Test_DockerRun_HostDaemon() {
	if s.IsCI() && !(goRuntime.GOOS == "linux" && goRuntime.GOARCH == "amd64") {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
	}
	require := s.Require()
	ctx := s.Context()

	svc := s.ServiceName()
	tag := "telepresence/echo-test"
	testDir := "testdata/echo-server"

	_, err := itest.Output(ctx, "docker", "build", "-t", tag, testDir)
	require.NoError(err)

	abs, err := filepath.Abs(testDir)
	require.NoError(err)

	runDockerRun := func(ctx context.Context, wch chan<- struct{}) {
		defer close(wch)
		_, stderr, _ := itest.Telepresence(ctx, "intercept", "--mount", "false", svc,
			"--docker-run", "--port", "9070:8080", "--", "--rm", "-v", abs+":/usr/src/app", tag)
		if len(stderr) > 0 {
			dlog.Debugf(ctx, "stderr = %q", stderr)
		}
	}

	assertInterceptResponse := func(ctx context.Context) {
		s.Eventually(func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			return err == nil && strings.Contains(stdout, svc+": intercepted")
		}, 30*time.Second, 3*time.Second)

		// Response contains env variables TELEPRESENCE_CONTAINER and TELEPRESENCE_INTERCEPT_ID
		expectedOutput := regexp.MustCompile(`Intercept id [0-9a-f-]+:` + svc)
		s.Eventually(
			// condition
			func() bool {
				out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "1", svc)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				dlog.Info(ctx, out)
				return expectedOutput.MatchString(out)
			},
			10*time.Second, // waitFor
			2*time.Second,  // polling interval
			`body of %q matches %q`, "http://"+svc, expectedOutput,
		)
	}

	assertNotIntercepted := func(ctx context.Context) {
		s.Eventually(func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			return err == nil && !strings.Contains(stdout, svc+": intercepted")
		}, 10*time.Second, 2*time.Second)
	}

	s.Run("<ctrl>-C", func() {
		// Use a soft context to send a <ctrl>-c to telepresence in order to end it
		ctx := s.Context()
		soft, softCancel := context.WithCancel(dcontext.WithSoftness(ctx))
		wch := make(chan struct{})
		go runDockerRun(soft, wch)
		assertInterceptResponse(ctx)
		softCancel()
		select {
		case <-wch:
		case <-time.After(30 * time.Second):
			itest.TelepresenceOk(ctx, "leave", svc)
			s.Fail("interceptor did not terminate")
		}
		assertNotIntercepted(ctx)
	})

	s.Run("leave", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		go runDockerRun(ctx, wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceOk(ctx, "leave", svc)
		select {
		case <-wch:
		case <-time.After(30 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		assertNotIntercepted(ctx)
	})

	s.Run("disconnect", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		go runDockerRun(ctx, wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceDisconnectOk(ctx)
		select {
		case <-wch:
		case <-time.After(30 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		s.TelepresenceConnect(ctx)
		assertNotIntercepted(ctx)
	})

	s.Run("quit", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		go runDockerRun(ctx, wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceQuitOk(ctx)
		select {
		case <-wch:
		case <-time.After(30 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		s.TelepresenceConnect(ctx)
		assertNotIntercepted(ctx)
	})
}

func (s *dockerDaemonSuite) Test_DockerRun_DockerDaemon() {
	if s.IsCI() && !(goRuntime.GOOS == "linux" && goRuntime.GOARCH == "amd64") {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
	}
	svc := "echo"
	ctx := s.Context()
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	require := s.Require()
	stdout := s.TelepresenceConnect(ctx, "--docker")
	defer itest.TelepresenceQuitOk(ctx)

	match := regexp.MustCompile(`Connected to context ?(.+),\s*namespace (\S+)\s+\(`).FindStringSubmatch(stdout)
	require.Len(match, 3)

	daemonID, err := daemon.NewIdentifier("", match[1], match[2], true)
	require.NoError(err)
	daemonName := daemonID.ContainerName()
	tag := "telepresence/echo-test"
	testDir := "testdata/echo-server"

	_, err = itest.Output(ctx, "docker", "build", "-t", tag, testDir)
	require.NoError(err)

	abs, err := filepath.Abs(testDir)
	require.NoError(err)

	runDockerRun := func(ctx context.Context, wch chan<- struct{}) {
		defer close(wch)
		_, _, _ = itest.Telepresence(ctx, "intercept", "--mount", "false", svc,
			"--docker-run", "--", "--rm", "-v", abs+":/usr/src/app", tag)
	}

	assertInterceptResponse := func(ctx context.Context) {
		s.Eventually(func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			return err == nil && strings.Contains(stdout, svc+": intercepted")
		}, 30*time.Second, 3*time.Second)

		expectedOutput := regexp.MustCompile(`Intercept id [0-9a-f-]+:` + svc)
		s.Eventually(
			// condition
			func() bool {
				out, err := itest.Output(ctx,
					"docker", "run", "--network", "container:"+daemonName, "--rm", "curlimages/curl", "--silent", "--max-time", "1", svc)
				if err != nil {
					dlog.Errorf(ctx, "%s:%v", out, err)
					return false
				}
				dlog.Info(ctx, out)
				return expectedOutput.MatchString(out)
			},
			10*time.Second, // waitFor
			2*time.Second,  // polling interval
			`body of %q matches %q`, "http://"+svc, expectedOutput,
		)
	}

	assertNotIntercepted := func(ctx context.Context) {
		s.Eventually(func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			return err == nil && !strings.Contains(stdout, svc+": intercepted")
		}, 10*time.Second, 2*time.Second)
	}

	s.Run("<ctrl>-C", func() {
		// Use a soft context to send a <ctrl>-c to telepresence in order to end it
		ctx := s.Context()
		soft, softCancel := context.WithCancel(dcontext.WithSoftness(ctx))
		wch := make(chan struct{})
		go runDockerRun(soft, wch)
		assertInterceptResponse(ctx)
		softCancel()
		assertNotIntercepted(ctx)
	})

	s.Run("leave", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		go runDockerRun(ctx, wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceOk(ctx, "leave", svc)
		select {
		case <-wch:
		case <-time.After(30 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		assertNotIntercepted(ctx)
	})

	s.Run("disconnect", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		go runDockerRun(ctx, wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceDisconnectOk(ctx)
		select {
		case <-wch:
		case <-time.After(30 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		s.TelepresenceConnect(ctx, "--docker")
		assertNotIntercepted(ctx)
	})

	s.Run("quit", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		go runDockerRun(ctx, wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceQuitOk(ctx)
		select {
		case <-wch:
		case <-time.After(30 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		s.TelepresenceConnect(ctx, "--docker")
		assertNotIntercepted(ctx)
	})
}

func (s *dockerDaemonSuite) Test_DockerRun_VolumePresent() {
	ctx := s.Context()
	s.KubectlOk(ctx, "create", "serviceaccount", testIamServiceAccount)
	defer s.KubectlOk(ctx, "delete", "serviceaccount", testIamServiceAccount)

	s.ApplyApp(ctx, "hello-w-volumes", "deploy/hello")
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "hello")

	s.TelepresenceConnect(ctx, "--docker")
	defer itest.TelepresenceQuitOk(ctx)

	stdout, stderr, err := itest.Telepresence(ctx, "intercept", "--docker-run", "hello", "-p", "8080:http", "--",
		"--rm", "busybox", "ls", "/var/run/secrets/datawire.io/auth")
	s.NoError(err)
	s.Empty(stderr)
	dlog.Infof(ctx, "stdout = %s", stdout)
	s.True(strings.HasSuffix(stdout, "\nusername"))
}

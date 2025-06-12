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

	runDockerRun := func(ctx context.Context, cancel context.CancelFunc) {
		defer cancel()
		_, stderr, _ := itest.Telepresence(ctx, "intercept", svc, "--docker-run", "--port", "9070:8080", "--", "--rm", "-v", abs+":/usr/src/app", tag)
		if len(stderr) > 0 {
			dlog.Debugf(ctx, "stderr = %q", stderr)
		}
	}

	assertInterceptResponse := func(ctx context.Context) {
		assert := s.Assert()
		assert.EventuallyContext(ctx, func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			return err == nil && strings.Contains(stdout, svc+": intercepted")
		}, 30*time.Second, 3*time.Second)

		// Response contains env variables TELEPRESENCE_CONTAINER and TELEPRESENCE_INTERCEPT_ID
		expectedOutput := regexp.MustCompile(`Intercept id [0-9a-f-]+:` + svc)
		assert.EventuallyContext(ctx, func() bool {
			out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "1", "http://"+svc)
			dlog.Info(ctx, out)
			if err != nil {
				dlog.Error(ctx, err)
				return false
			}
			return expectedOutput.MatchString(out)
		},
			30*time.Second, // waitFor
			2*time.Second,  // polling interval
			`body of %q matches %q`, "http://"+svc, expectedOutput,
		)
	}

	assertNotIntercepted := func(ctx context.Context) {
		assert := s.Assert()
		assert.EventuallyContext(ctx, func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			if err != nil {
				dlog.Error(ctx, err)
				return false
			}
			if strings.Contains(stdout, svc+": intercepted") {
				dlog.Debugf(ctx, "stdout: %q", stdout)
				return false
			}
			return true
		}, 30*time.Second, 2*time.Second)
	}

	s.Run("<ctrl>-C", func() {
		// Use a soft context to send a <ctrl>-c to telepresence in order to end it
		ctx := s.Context()
		cctx, cancel := context.WithCancel(ctx)
		soft, softCancel := context.WithCancel(dcontext.WithSoftness(ctx))
		go runDockerRun(soft, cancel)
		assertInterceptResponse(cctx)
		softCancel()
		select {
		case <-cctx.Done():
		case <-time.After(30 * time.Second):
			itest.TelepresenceOk(ctx, "leave", svc)
			s.Fail("interceptor did not terminate")
		}
		assertNotIntercepted(ctx)
	})

	s.Run("leave", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		cctx, cancel := context.WithCancel(ctx)
		go runDockerRun(ctx, cancel)
		assertInterceptResponse(cctx)
		itest.TelepresenceOk(ctx, "leave", svc)
		select {
		case <-cctx.Done():
		case <-time.After(30 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		assertNotIntercepted(ctx)
	})

	s.Run("disconnect", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		cctx, cancel := context.WithCancel(ctx)
		go runDockerRun(ctx, cancel)
		assertInterceptResponse(cctx)
		itest.TelepresenceDisconnectOk(ctx)
		select {
		case <-cctx.Done():
		case <-time.After(30 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		s.TelepresenceConnect(ctx)
		assertNotIntercepted(ctx)
	})

	s.Run("quit", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		cctx, cancel := context.WithCancel(ctx)
		go runDockerRun(ctx, cancel)
		assertInterceptResponse(cctx)
		itest.TelepresenceQuitOk(ctx)
		select {
		case <-cctx.Done():
		case <-time.After(30 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		s.TelepresenceConnect(ctx)
		assertNotIntercepted(ctx)
	})
}

func (s *dockerDaemonSuite) Test_DockerRun_DockerDaemon() {
	svc := "echo"
	ctx := s.Context()
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	require := s.Require()
	stdout := s.TelepresenceConnect(ctx, "--docker")
	defer itest.TelepresenceQuitOk(ctx)

	match := regexp.MustCompile(`Connected to context ?(.+),\s*namespace (\S+)\s+\(`).FindStringSubmatch(stdout)
	require.Len(match, 3)

	tag := "telepresence/echo-test"
	testDir := "testdata/echo-server"

	_, err := itest.Output(ctx, "docker", "build", "-t", tag, testDir)
	require.NoError(err)

	abs, err := filepath.Abs(testDir)
	require.NoError(err)

	runDockerRun := func(ctx context.Context, name string, wch chan<- struct{}) {
		defer close(wch)
		so, se, err := itest.Telepresence(ctx, "intercept", "--mount", "false", svc,
			"--docker-run", "--", "--name", name, "--rm", "-v", abs+":/usr/src/app", tag)
		dlog.Info(ctx, so)
		if se != "" {
			dlog.Error(ctx, se)
		}
		if err != nil {
			dlog.Error(ctx, err.Error())
		}
	}

	assertInterceptResponse := func(ctx context.Context) {
		s.Eventually(func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			dlog.Info(ctx, stdout)
			return err == nil && strings.Contains(stdout, svc+": intercepted")
		}, 30*time.Second, 3*time.Second)

		expectedOutput := regexp.MustCompile(`Intercept id [0-9a-f-]+:` + svc)
		s.Eventually(
			// condition
			func() bool {
				so, _, err := itest.Telepresence(ctx, "curl", "--silent", "--max-time", "2", "http://"+svc)
				dlog.Info(ctx, so)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				return expectedOutput.MatchString(so)
			},
			60*time.Second, // A docker container reuses IPs, but MAC-address changes. It takes time for the network to learn about this.
			5*time.Second,  // polling interval
			`body of %q matches %q`, "http://"+svc, expectedOutput,
		)
	}

	assertNotIntercepted := func(ctx context.Context) {
		s.Eventually(func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			return err == nil && !strings.Contains(stdout, svc+": intercepted")
		}, 15*time.Second, 2*time.Second)
	}

	s.Run("<ctrl>-C", func() {
		// Use a soft context to send a <ctrl>-c to telepresence in order to end it
		ctx := s.Context()
		soft, softCancel := context.WithCancel(dcontext.WithSoftness(ctx))
		wch := make(chan struct{})
		go runDockerRun(soft, "adam", wch)
		s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
		assertInterceptResponse(ctx)
		softCancel()
		assertNotIntercepted(ctx)
	})

	s.Run("leave", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		go runDockerRun(ctx, "bruce", wch)
		s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
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
		go runDockerRun(ctx, "chris", wch)
		s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
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
		go runDockerRun(ctx, "david", wch)
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
	s.ApplyTemplate(ctx, filepath.Join("testdata", "k8s", "hello-w-volumes.goyaml"), nil)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "hello")

	s.TelepresenceConnect(ctx, "--docker")
	defer itest.TelepresenceQuitOk(ctx)

	stdout, _, err := itest.Telepresence(ctx, "intercept", "--docker-run", "hello", "-p", "8080:http", "--",
		"--rm", "busybox", "ls", "/var/run/secrets/datawire.io/auth")
	s.NoError(err)
	dlog.Infof(ctx, "stdout = %s", stdout)
	s.True(strings.HasSuffix(stdout, "\nusername"))
}

func (s *dockerDaemonSuite) Test_DockerRunCommand() {
	ctx := s.Context()
	require := s.Require()
	s.TelepresenceConnect(ctx, "--docker", "--hostname", "cicero")
	defer itest.TelepresenceQuitOk(ctx)

	stdout, _, err := itest.Telepresence(ctx, "docker-run", "--rm", "busybox", "ip", "r")
	require.NoError(err)
	dlog.Infof(ctx, "stdout = %s", stdout)
	if s.ClientIsVersion(">=2.23.0") {
		s.Contains(stdout, "dev tpd-0")
	}
}

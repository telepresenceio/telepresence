package integration_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	goRuntime "runtime"
	"strings"
	"time"

	"github.com/telepresenceio/dlib/v2/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func runDockerRun(ctx context.Context, name, svc, port, appDir, tag string, rq *itest.Requirements, wch chan<- struct{}) *os.Process {
	_ = itest.Run(ctx, "docker", "container", "stop", name)
	args := []string{"intercept", "--mount", "false", svc, "--docker-run"}
	if port != "" {
		args = append(args, "--port", port)
	}
	args = append(args, "--", "--rm", "-v", appDir+":/usr/src/app")
	if name != "" {
		args = append(args, "--name", name)
	}
	args = append(args, tag)
	cmd := itest.TelepresenceCmd(ctx, args...)
	so := &bytes.Buffer{}
	se := &bytes.Buffer{}
	cmd.Stdout = so
	cmd.Stderr = se
	rq.NoError(cmd.Start())
	proc := cmd.Process
	go func() {
		if wch != nil {
			defer close(wch)
		}
		err := cmd.Wait()
		dlog.Info(ctx, so.String())
		if ses := se.String(); ses != "" {
			dlog.Error(ctx, ses)
		}
		if err != nil {
			dlog.Error(ctx, err.Error())
		}
	}()
	return proc
}

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

	port := "9070:8080"
	s.Run("<ctrl>-C", func() {
		// Use a soft context to send a <ctrl>-c to telepresence in order to end it
		ctx := s.Context()
		wch := make(chan struct{})
		proc := runDockerRun(ctx, "", svc, port, abs, tag, s.Require(), wch)
		assertInterceptResponse(ctx)
		_ = proc.Signal(os.Interrupt)
		select {
		case <-wch:
		case <-time.After(10 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		assertNotIntercepted(ctx)
	})

	s.Run("leave", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		runDockerRun(ctx, "", svc, port, abs, tag, s.Require(), wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceOk(ctx, "leave", svc)
		select {
		case <-wch:
		case <-time.After(10 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		assertNotIntercepted(ctx)
	})

	s.Run("disconnect", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		runDockerRun(ctx, "", svc, port, abs, tag, s.Require(), wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceDisconnectOk(ctx)
		select {
		case <-wch:
		case <-time.After(10 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		s.TelepresenceConnect(ctx)
		assertNotIntercepted(ctx)
	})

	s.Run("quit", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		runDockerRun(ctx, "", svc, port, abs, tag, s.Require(), wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceQuitOk(ctx)
		select {
		case <-wch:
		case <-time.After(10 * time.Second):
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
		wch := make(chan struct{})
		proc := runDockerRun(ctx, "adam", svc, "", abs, tag, s.Require(), wch)
		s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
		assertInterceptResponse(ctx)
		_ = proc.Signal(os.Interrupt)
		select {
		case <-wch:
		case <-time.After(10 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		assertNotIntercepted(ctx)
	})

	s.Run("leave", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		runDockerRun(ctx, "bruce", svc, "", abs, tag, s.Require(), wch)
		s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
		assertInterceptResponse(ctx)
		itest.TelepresenceOk(ctx, "leave", svc)
		select {
		case <-wch:
		case <-time.After(10 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		assertNotIntercepted(ctx)
	})

	s.Run("disconnect", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		runDockerRun(ctx, "chris", svc, "", abs, tag, s.Require(), wch)
		s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
		assertInterceptResponse(ctx)
		itest.TelepresenceDisconnectOk(ctx)
		select {
		case <-wch:
		case <-time.After(10 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		s.TelepresenceConnect(ctx, "--docker")
		assertNotIntercepted(ctx)
	})

	s.Run("quit", func() {
		// End the intercept from another telepresence invocation
		ctx := s.Context()
		wch := make(chan struct{})
		runDockerRun(ctx, "david", svc, "", abs, tag, s.Require(), wch)
		assertInterceptResponse(ctx)
		itest.TelepresenceQuitOk(ctx)
		select {
		case <-wch:
		case <-time.After(10 * time.Second):
			s.Fail("interceptor did not terminate")
		}
		s.TelepresenceConnect(ctx, "--docker")
		assertNotIntercepted(ctx)
	})
}

func (s *dockerDaemonSuite) Test_DockerRun_VolumePresent() {
	if !s.ClientIsVersion(">2.24.x") {
		s.T().Skip("Not part of compatibility tests. Docker volume plugin is unstable for versions < 2.25.0")
	}
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

func (s *dockerDaemonSuite) Test_DockerRunExternalDNS() {
	ctx := s.Context()
	require := s.Require()
	s.TelepresenceConnect(ctx, "--docker")
	defer itest.TelepresenceQuitOk(ctx)

	stdout, _, err := itest.Telepresence(ctx, "docker-run", "--rm", "busybox", "nslookup", "google.com")
	require.NoError(err)
	dlog.Infof(ctx, "stdout = %s", stdout)
	s.Contains(stdout, "Address: ")
}

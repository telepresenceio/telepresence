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

func (s *singleServiceSuite) Test_DockerRun() {
	if s.IsCI() && goRuntime.GOOS != "linux" {
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
		_, _, _ = itest.Telepresence(ctx, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", svc,
			"--docker-run", "--port", "9070:8080", "--", "--rm", "-v", abs+":/usr/src/app", tag)
	}

	assertInterceptResponse := func(ctx context.Context) {
		s.Eventually(func() bool {
			stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
			return strings.Contains(stdout, svc+": intercepted")
		}, 10*time.Second, 3*time.Second)

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
			stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
			return !strings.Contains(stdout, svc+": intercepted")
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
		itest.TelepresenceOk(ctx, "leave", svc+"-"+s.AppNamespace())
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
		itest.TelepresenceOk(ctx, "connect")
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
		itest.TelepresenceOk(ctx, "connect")
		assertNotIntercepted(ctx)
	})
}

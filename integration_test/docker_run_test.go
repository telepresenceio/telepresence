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
	// This test only runs on linux as it requires docker, and CI can't run linux docker containers inside non-linux runners
	if s.IsCI() && goRuntime.GOOS != "linux" {
		s.T().SkipNow()
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

	// Use a soft context to send a <ctrl>-c to telepresence in order to end it
	soft, softCancel := context.WithCancel(dcontext.WithSoftness(ctx))
	cmd := itest.TelepresenceCmd(soft, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", svc,
		"--docker-run", "--port", "9070:8080", "--", "--rm", "-v", abs+":/usr/src/app", tag)
	out := dlog.StdLogger(ctx, dlog.LogLevelDebug).Writer()
	cmd.Stdout = out
	cmd.Stderr = out
	require.NoError(cmd.Start())

	s.Eventually(func() bool {
		stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
		return strings.Contains(stdout, svc+": intercepted")
	}, 10*time.Second, 2*time.Second)

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
		30*time.Second, // waitFor
		3*time.Second,  // polling interval
		`body of %q matches %q`, "http://"+svc, expectedOutput,
	)
	softCancel()
	_, _, _ = itest.Telepresence(ctx, "leave", svc+"-"+s.AppNamespace()) //nolint:dogsled // don't care about success or failure
}

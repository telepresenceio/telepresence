package integration_test

import (
	"regexp"
	"strings"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *notConnectedSuite) Test_Uninstall() {
	require := s.Require()
	ctx := itest.WithUser(s.Context(), "default")
	itest.TelepresenceOk(ctx, "connect")

	names := func() (string, error) {
		return itest.KubectlOut(ctx, s.ManagerNamespace(),
			"get", "svc,deploy", "traffic-manager",
			"--ignore-not-found",
			"-o", "jsonpath={.items[*].metadata.name}")
	}

	stdout, err := names()
	require.NoError(err)
	require.Equal(2, len(strings.Split(stdout, " ")), "the string %q doesn't contain a service and a deployment", stdout)
	s.CapturePodLogs(ctx, "app=traffic-manager", "", s.ManagerNamespace())

	// Add webhook agent to test webhook uninstall
	jobname := "echo-auto-inject"
	deployname := "deploy/" + jobname
	s.ApplyApp(ctx, jobname, deployname)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", jobname)

	s.Eventually(func() bool {
		stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--agents")
		return strings.Contains(stdout, jobname+": ready to intercept (traffic-agent already installed)")
	}, 30*time.Second, 3*time.Second)

	// The telepresence-test-developer will not be able to uninstall everything
	stdout = itest.TelepresenceOk(ctx, "helm", "uninstall")
	defer s.installTelepresence(ctx)
	itest.AssertQuitOutput(ctx, stdout)

	// Double check webhook agent is uninstalled
	require.NoError(s.RolloutStatusWait(ctx, deployname))
	s.Eventually(func() bool {
		stdout, err = s.KubectlOut(ctx, "get", "pods")
		if err != nil {
			dlog.Error(ctx, err)
			return false
		}
		match, err := regexp.MatchString(jobname+`-[a-z0-9]+-[a-z0-9]+\s+1/1\s+Running`, stdout)
		return err == nil && match
	}, itest.PodCreateTimeout(ctx), 2*time.Second)

	require.Eventually(
		func() bool {
			stdout, _ := names()
			return stdout == ""
		},
		5*time.Second,        // waitFor
		500*time.Millisecond, // polling interval
	)
}

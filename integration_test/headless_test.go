package integration_test

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/datawire/dlib/dcontext"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) Test_SuccessfullyInterceptsHeadlessService() {
	if itest.GetProfile(s.Context()) == itest.GkeAutopilotProfile {
		s.T().Skip("GKE Autopilot does not support NET_ADMIN containers which means headless services can't be intercepted")
	}
	ctx, cancel := context.WithCancel(dcontext.WithSoftness(s.Context()))
	defer cancel()
	const svc = "echo-headless"

	svcPort, svcCancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer svcCancel()

	s.ApplyApp(ctx, "echo-headless", "statefulset/echo-headless")
	defer s.DeleteSvcAndWorkload(ctx, "statefulset", "echo-headless")
	require := s.Require()
	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", svc, "--port", strconv.Itoa(svcPort))
	require.Contains(stdout, "Using StatefulSet echo-headless")
	s.CapturePodLogs(ctx, "echo-headless", "traffic-agent", s.AppNamespace())

	defer itest.TelepresenceOk(ctx, "leave", "echo-headless")

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
			return err == nil && strings.Contains(stdout, "echo-headless: intercepted")
		},
		30*time.Second, // waitFor
		3*time.Second,  // polling interval
		`intercepted workload never show up in list`)

	itest.PingInterceptedEchoServer(ctx, svc, "8080")
}

package integration_test

import (
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) successfulIntercept(tp, svc, port string) {
	ctx := s.Context()
	s.ApplyApp(ctx, svc, strings.ToLower(tp)+"/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "echo-auto-inject")

	require := s.Require()

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list")
			return err == nil && strings.Contains(stdout, svc)
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
	)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "--port", port, svc)
	require.Contains(stdout, "Using "+tp+" "+svc)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.Contains(stdout, svc+": intercepted")
	require.NotContains(stdout, "Volume Mount Point")
	s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())
	itest.TelepresenceOk(ctx, "leave", svc)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.NotContains(stdout, svc+": intercepted")

	itest.TelepresenceDisconnectOk(ctx)

	dfltCtx := itest.WithUser(ctx, "default")
	itest.TelepresenceOk(dfltCtx, "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
	itest.TelepresenceOk(dfltCtx, "uninstall", "--agent", svc)
	itest.TelepresenceDisconnectOk(dfltCtx)
	s.TelepresenceConnect(ctx)

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--agents")
			return err == nil && !strings.Contains(stdout, svc)
		},
		180*time.Second, // waitFor
		6*time.Second,   // polling interval
	)
}

func (s *connectedSuite) Test_SuccessfullyInterceptsDeploymentWithProbes() {
	s.successfulIntercept("Deployment", "with-probes", "9090")
}

func (s *connectedSuite) Test_SuccessfullyInterceptsReplicaSet() {
	s.successfulIntercept("ReplicaSet", "rs-echo", "9091")
}

func (s *connectedSuite) Test_SuccessfullyInterceptsStatefulSet() {
	s.successfulIntercept("StatefulSet", "ss-echo", "9092")
}

func (s *connectedSuite) Test_SuccessfullyInterceptsDeploymentWithNoVolumes() {
	s.successfulIntercept("Deployment", "echo-no-vols", "9093")
}

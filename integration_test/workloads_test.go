package integration_test

import (
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) successfulIntercept(tp, wl, port string) {
	ctx := s.Context()
	s.ApplyApp(ctx, wl, strings.ToLower(tp)+"/"+wl)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", "echo-auto-inject")

	require := s.Require()

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list")
			return err == nil && strings.Contains(stdout, wl)
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
	)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "--port", port, wl)
	require.Contains(stdout, "Using "+tp+" "+wl)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.Contains(stdout, wl+": intercepted")
	require.NotContains(stdout, "Volume Mount Point")
	s.CapturePodLogs(ctx, wl, "traffic-agent", s.AppNamespace())
	itest.TelepresenceOk(ctx, "leave", wl)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.NotContains(stdout, wl+": intercepted")

	itest.TelepresenceDisconnectOk(ctx)

	dfltCtx := itest.WithUser(ctx, "default")
	itest.TelepresenceOk(dfltCtx, "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
	itest.TelepresenceOk(dfltCtx, "uninstall", "--agent", wl)
	itest.TelepresenceDisconnectOk(dfltCtx)
	s.TelepresenceConnect(ctx)

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--agents")
			return err == nil && !strings.Contains(stdout, wl)
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

func (s *connectedSuite) Test_SuccessfullyInterceptsDeploymentWithoutService() {
	s.successfulIntercept("Deployment", "echo-no-svc", "9094")
}

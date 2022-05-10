package integration_test

import (
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) successfulIntercept(tp, svc, port string) {
	ctx := s.Context()
	s.ApplyApp(ctx, svc, strings.ToLower(tp)+"/"+svc)
	defer func() {
		_ = s.Kubectl(ctx, "delete", "svc,"+strings.ToLower(tp), svc)
	}()
	require := s.Require()

	require.Eventually(
		func() bool {
			return strings.Contains(itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace()), svc)
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
	)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", "--port", port, svc)
	require.Contains(stdout, "Using "+tp+" "+svc)
	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
	require.Contains(stdout, svc+": intercepted")
	require.NotContains(stdout, "Volume Mount Point")
	s.CapturePodLogs(ctx, "service="+svc, "traffic-agent", s.AppNamespace())
	itest.TelepresenceOk(ctx, "leave", svc+"-"+s.AppNamespace())
	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
	require.NotContains(stdout, svc+": intercepted")

	itest.TelepresenceQuitOk(ctx)

	dfltCtx := itest.WithUser(ctx, "default")
	itest.TelepresenceOk(dfltCtx, "uninstall", "--namespace", s.AppNamespace(), "--agent", svc)
	itest.TelepresenceQuitOk(dfltCtx)
	itest.TelepresenceOk(ctx, "connect")

	require.Eventually(
		func() bool {
			return !strings.Contains(itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--agents"), svc)
		},
		120*time.Second, // waitFor
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

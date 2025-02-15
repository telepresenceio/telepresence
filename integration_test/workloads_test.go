package integration_test

import (
	"strings"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) successfulIntercept(tp, wl, port string) {
	ctx := s.Context()
	s.ApplyApp(ctx, wl, strings.ToLower(tp)+"/"+wl)
	defer s.DeleteSvcAndWorkload(ctx, tp, wl)

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
	itest.TelepresenceOk(dfltCtx, "uninstall", wl)
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

func (s *connectedSuite) successfulIngest(tp, wl string) {
	ctx := s.Context()
	s.ApplyApp(ctx, wl, strings.ToLower(tp)+"/"+wl)
	defer s.DeleteSvcAndWorkload(ctx, tp, wl)

	require := s.Require()

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list")
			return err == nil && strings.Contains(stdout, wl)
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
	)

	stdout := itest.TelepresenceOk(ctx, "ingest", "--mount", "false", wl)
	require.Contains(stdout, "Using "+tp+" "+wl)
	stdout = itest.TelepresenceOk(ctx, "list", "--ingests")
	require.Contains(stdout, wl+": ingested")
	require.NotContains(stdout, "Volume Mount Point")
	s.CapturePodLogs(ctx, wl, "traffic-agent", s.AppNamespace())
	itest.TelepresenceOk(ctx, "leave", wl)
	stdout = itest.TelepresenceOk(ctx, "list", "--ingests")
	require.NotContains(stdout, wl+": ingested")

	itest.TelepresenceDisconnectOk(ctx)

	dfltCtx := itest.WithUser(ctx, "default")
	itest.TelepresenceOk(dfltCtx, "connect", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
	itest.TelepresenceOk(dfltCtx, "uninstall", wl)
	itest.TelepresenceDisconnectOk(dfltCtx)
	s.TelepresenceConnect(ctx)

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--agents")
			if err != nil {
				dlog.Error(ctx, err)
				return false
			}
			if strings.Contains(stdout, wl) {
				dlog.Errorf(ctx, "Expected %q to not contain %q", stdout, wl)
				return false
			}
			return true
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
	if !s.ManagerIsVersion(">2.21.x") {
		s.T().Skip("Not part of compatibility tests. StatefulSet rollouts fail intermittently in versions < 2.22.0")
	}
	s.successfulIntercept("StatefulSet", "ss-echo", "9092")
}

func (s *connectedSuite) Test_SuccessfullyInterceptsDeploymentWithNoVolumes() {
	s.successfulIntercept("Deployment", "echo-no-vols", "9093")
}

func (s *connectedSuite) Test_SuccessfullyInterceptsDeploymentWithoutService() {
	s.successfulIntercept("Deployment", "echo-no-svc-ann", "9094")
}

func (s *connectedSuite) Test_SuccessfullyIngestsDeploymentWithProbes() {
	s.successfulIngest("Deployment", "with-probes")
}

func (s *connectedSuite) Test_SuccessfullyIngestsReplicaSet() {
	s.successfulIngest("ReplicaSet", "rs-echo")
}

func (s *connectedSuite) Test_SuccessfullyIngestsStatefulSet() {
	if !s.ManagerIsVersion(">2.21.x") {
		s.T().Skip("Not part of compatibility tests. StatefulSet rollouts fail intermittently in versions < 2.22.0")
	}
	s.successfulIngest("StatefulSet", "ss-echo")
}

func (s *connectedSuite) Test_SuccessfullyIngestsDeploymentWithNoVolumes() {
	s.successfulIngest("Deployment", "echo-no-vols")
}

func (s *connectedSuite) Test_SuccessfullyIngestsDeploymentWithoutService() {
	s.successfulIngest("Deployment", "echo-no-svc")
}

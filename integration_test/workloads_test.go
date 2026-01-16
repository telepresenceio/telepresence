package integration_test

import (
	"bytes"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

func (s *connectedSuite) uninstall(wl string) {
	ctx := s.Context()

	itest.TelepresenceOk(ctx, "uninstall", wl)
	s.Require().Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list", "--agents")
			return err == nil && !strings.Contains(stdout, wl)
		},
		180*time.Second, // waitFor
		6*time.Second,   // polling interval
	)
}

func (s *connectedSuite) successfulIntercept(tp, wl, port string) {
	ctx := s.Context()
	s.ApplyApp(ctx, wl, strings.ToLower(tp)+"/"+wl)
	defer s.DeleteSvcAndWorkload(ctx, tp, wl)

	s.doIntercept(tp, wl, port)
	if !s.ClientIsVersion(">2.21.x") && s.ManagerIsVersion(">2.21.x") {
		// An <2.22.0 client will not be able to uninstall an agent when the traffic-manager is >=2.22.0
		// because the client will attempt to remove the entry in the telepresence-agents configmap. It
		// is no longer present in versions >=2.22.0
		return
	}
	s.uninstall(wl)
	tpl := itest.DisruptionBudget{
		Name:         "telepresence-test",
		MinAvailable: 1,
	}

	// Do the intercept again, this time with a disruption budget with minAvailable=1 in place.
	rq := s.Require()
	db, err := itest.ReadTemplate(ctx, filepath.Join("testdata", "k8s", "disruption-budget.goyaml"), &tpl)
	rq.NoError(err)
	rq.NoError(s.Kubectl(dos.WithStdin(ctx, bytes.NewReader(db)), "apply", "-f", "-"))
	defer func() {
		_ = s.Kubectl(dos.WithStdin(ctx, bytes.NewReader(db)), "delete", "-f", "-")
	}()
	s.doIntercept(tp, wl, port)
}

func (s *connectedSuite) doIntercept(tp, wl, port string) {
	ctx := s.Context()
	require := s.Require()

	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list")
			return err == nil && strings.Contains(stdout, wl)
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
	)

	out, err := s.KubectlOut(ctx, "get", strings.ToLower(tp), wl, "-o", "jsonpath={.spec.replicas}")
	require.NoError(err)
	replicas, err := strconv.Atoi(out)
	require.NoError(err)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", "--port", port, wl)
	require.Contains(stdout, "Using "+tp+" "+wl)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.Contains(stdout, wl+": intercepted")
	require.NotContains(stdout, "Volume Mount Point")
	s.Eventually(func() bool {
		ras := itest.RunningPodsWithAgents(ctx, wl, s.AppNamespace())
		clog.Infof(ctx, "pod with agent count %d, expected %d", len(ras), replicas)
		return len(ras) == replicas
	}, 60*time.Second, 5*time.Second)
	s.CapturePodLogs(ctx, wl, "traffic-agent", s.AppNamespace())
	time.Sleep(10 * time.Second)
	itest.TelepresenceOk(ctx, "leave", wl)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.NotContains(stdout, wl+": intercepted")
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
	if !s.ClientIsVersion(">2.21.x") && s.ManagerIsVersion(">2.21.x") {
		// An <2.22.0 client will not be able to uninstall an agent when the traffic-manager is >=2.22.0
		// because the client will attempt to remove the entry in the telepresence-agents configmap. It
		// is no longer present in versions >=2.22.0
		return
	}
	s.uninstall(wl)
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

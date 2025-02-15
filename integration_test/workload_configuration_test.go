package integration_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type workloadConfigurationSuite struct {
	itest.Suite
	itest.TrafficManager
}

func (s *workloadConfigurationSuite) SuiteName() string {
	return "WorkloadConfiguration"
}

func init() {
	itest.AddTrafficManagerSuite("-workload-configuration", func(h itest.TrafficManager) itest.TestingSuite {
		return &workloadConfigurationSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (s *workloadConfigurationSuite) disabledWorkloadKind(tp, wl string) {
	ctx := s.Context()
	require := s.Require()

	s.ApplyApp(ctx, wl, strings.ToLower(tp)+"/"+wl)
	defer s.DeleteSvcAndWorkload(ctx, strings.ToLower(tp), wl)

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)

	// give it time for the workload to be detected (if it was going to be)
	time.Sleep(6 * time.Second)

	list := itest.TelepresenceOk(ctx, "list")
	require.Equal("No Workloads (Deployments, StatefulSets, ReplicaSets, or Rollouts)", list)

	_, stderr, err := itest.Telepresence(ctx, "intercept", wl)
	require.Error(err)
	require.Contains(stderr, fmt.Sprintf("connector.CreateIntercept: workload \"%s.%s\" not found", wl, s.AppNamespace()))
}

func (s *workloadConfigurationSuite) Test_DisabledReplicaSet() {
	s.TelepresenceHelmInstallOK(s.Context(), true, "--set", "workloads.replicaSets.enabled=false")
	defer s.TelepresenceHelmInstallOK(s.Context(), true, "--set", "workloads.replicaSets.enabled=true")
	s.disabledWorkloadKind("ReplicaSet", "rs-echo")
}

func (s *workloadConfigurationSuite) Test_DisabledStatefulSet() {
	s.TelepresenceHelmInstallOK(s.Context(), true, "--set", "workloads.statefulSets.enabled=false")
	defer s.TelepresenceHelmInstallOK(s.Context(), true, "--set", "workloads.statefulSets.enabled=true")
	s.disabledWorkloadKind("StatefulSet", "ss-echo")
}

func (s *workloadConfigurationSuite) Test_InterceptsDeploymentWithDisabledReplicaSets() {
	ctx := s.Context()
	require := s.Require()

	wl, tp := "echo-one", "Deployment"
	s.ApplyApp(ctx, wl, strings.ToLower(tp)+"/"+wl)
	defer s.DeleteSvcAndWorkload(ctx, strings.ToLower(tp), wl)

	s.TelepresenceHelmInstallOK(ctx, true, "--set", "workloads.replicaSets.enabled=false")
	defer s.TelepresenceHelmInstallOK(ctx, true, "--set", "workloads.replicaSets.enabled=true")

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)

	verb := "engage"
	if !s.ClientIsVersion(">2.21.x") {
		verb = "intercept"
	}
	require.Eventually(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list")
			return err == nil && strings.Contains(stdout, fmt.Sprintf("%s: ready to %s", wl, verb))
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
	)

	stdout := itest.TelepresenceOk(ctx, "intercept", wl)
	require.Contains(stdout, fmt.Sprintf("Using %s %s", tp, wl))

	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.Contains(stdout, fmt.Sprintf("%s: intercepted", wl))
	itest.TelepresenceOk(ctx, "leave", wl)
}

func (s *workloadConfigurationSuite) Test_InterceptsReplicaSetWithDisabledDeployments() {
	ctx := s.Context()
	require := s.Require()

	wl, tp := "echo-easy", "Deployment"
	s.ApplyApp(ctx, wl, strings.ToLower(tp)+"/"+wl)
	defer s.DeleteSvcAndWorkload(ctx, strings.ToLower(tp), wl)

	interceptableWl := s.KubectlOk(ctx, "get", "replicasets", "-l", fmt.Sprintf("app=%s", wl), "-o", "jsonpath={.items[*].metadata.name}")

	s.TelepresenceHelmInstallOK(ctx, true, "--set", "logLevel=trace", "--set", "workloads.deployments.enabled=false")
	defer s.TelepresenceHelmInstallOK(ctx, true, "--set", "workloads.deployments.enabled=true")

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceDisconnectOk(ctx)

	verb := "engage"
	if !s.ClientIsVersion(">2.21.x") {
		verb = "intercept"
	}
	expect := fmt.Sprintf("%s: ready to %s", interceptableWl, verb)
	require.Eventuallyf(
		func() bool {
			stdout, _, err := itest.Telepresence(ctx, "list")
			dlog.Info(ctx, stdout)
			return err == nil && strings.Contains(stdout, expect)
		},
		6*time.Second, // waitFor
		2*time.Second, // polling interval
		"expected %q was never produced", expect,
	)

	stdout := itest.TelepresenceOk(ctx, "intercept", interceptableWl)
	require.Contains(stdout, fmt.Sprintf("Using %s %s", "ReplicaSet", interceptableWl))

	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.Contains(stdout, fmt.Sprintf("%s: intercepted", interceptableWl))
	itest.TelepresenceOk(ctx, "leave", interceptableWl)
}

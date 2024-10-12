package integration_test

import (
	"os"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type agentInjectorDisabledSuite struct {
	itest.Suite
	itest.NamespacePair
	logName string
}

func (s *agentInjectorDisabledSuite) SuiteName() string {
	return "AgentInjectorDisabled"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &agentInjectorDisabledSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *agentInjectorDisabledSuite) SetupSuite() {
	s.Suite.SetupSuite()
	s.logName = s.TelepresenceHelmInstallOK(s.Context(), false, "--set", "agentInjector.enabled=false")
}

func (s *agentInjectorDisabledSuite) TearDownSuite() {
	s.UninstallTrafficManager(s.Context(), s.ManagerNamespace())
}

func (s *agentInjectorDisabledSuite) Test_AgentInjectorDisabled() {
	const svc = "echo-easy"
	ctx := s.Context()

	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	s.TelepresenceConnect(ctx)
	_, sErr, err := itest.Telepresence(ctx, "intercept", svc)
	s.Error(err)
	s.Contains(sErr, "agent-injector is disabled")
	itest.TelepresenceQuitOk(ctx)

	logData, err := os.ReadFile(s.logName)
	s.Require().NoError(err)

	logs := string(logData)
	s.NotContains(logs, "Using traffic-agent image")
	s.Contains(logs, "Cluster domain derived from /etc/resolv.conf")
}

func (s *agentInjectorDisabledSuite) Test_VersionWithAgentInjectorDisabled() {
	ctx := s.Context()
	rq := s.Require()
	restartCount := func() int {
		pods := itest.RunningPods(ctx, "traffic-manager", s.ManagerNamespace())
		if len(pods) == 1 {
			for _, cs := range pods[0].Status.ContainerStatuses {
				if cs.Name == "traffic-manager" {
					return int(cs.RestartCount)
				}
			}
		}
		return -1
	}
	oldRestartCount := restartCount()
	rq.GreaterOrEqual(oldRestartCount, 0)
	s.TelepresenceConnect(ctx)
	sr, err := itest.TelepresenceStatus(ctx)
	itest.TelepresenceQuitOk(ctx)
	rq.NoError(err)
	tm := sr.TrafficManager
	rq.NotNil(tm)
	rq.Empty(tm.TrafficAgent)

	// Verify that traffic-manager didn't crash
	rq.Equal(oldRestartCount, restartCount())
}

func (s *agentInjectorDisabledSuite) Test_ManualAgent() {
	s.TelepresenceConnect(s.Context())
	defer itest.TelepresenceQuitOk(s.Context())
	testManualAgent(&s.Suite, s.NamespacePair)
}

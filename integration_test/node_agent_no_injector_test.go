package integration_test

import (
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

// nodeAgentNoInjectorSuite exercises node-agent intercepts and ingests when
// the agent-injector is disabled (agentInjector.enabled=false). Node-agent
// mode does not depend on the mutating webhook, so intercepts and ingests
// must still work; a plain (sidecar) intercept must still fail with the
// established "agent-injector is disabled" error.
//
// This suite shares its setup shape and its intercept/ingest assertions with
// nodeAgentSuite (node_agent_test.go) via the embedded nodeAgentBase; only
// the Helm install flags and the extra "plain intercept fails" test differ.
type nodeAgentNoInjectorSuite struct {
	nodeAgentBase
}

func (s *nodeAgentNoInjectorSuite) SuiteName() string {
	return "NodeAgentNoInjector"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &nodeAgentNoInjectorSuite{nodeAgentBase{Suite: itest.Suite{Harness: h}, NamespacePair: h}}
	})
}

func (s *nodeAgentNoInjectorSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	s.reapLeftoverNodeAgentJobs(ctx)
	s.skipUnlessNodeAgentPodSecurityOK(ctx)

	s.TelepresenceHelmInstallOK(ctx, false, "--set", "agentInjector.enabled=false", "--set", "nodeAgent.enabled=true")
	s.ApplyApp(ctx, "echo-easy", "deploy/echo-easy")
	s.TelepresenceConnect(ctx)
}

func (s *nodeAgentNoInjectorSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
}

// Test_NodeAgentIntercept verifies that a node-agent intercept works
// end-to-end even though the agent-injector is disabled.
func (s *nodeAgentNoInjectorSuite) Test_NodeAgentIntercept() {
	s.assertNodeAgentIntercept("echo-easy")
}

// Test_NodeAgentIngest verifies that a node-agent ingest works end-to-end
// even though the agent-injector is disabled.
func (s *nodeAgentNoInjectorSuite) Test_NodeAgentIngest() {
	s.assertNodeAgentIngest("echo-easy")
}

// Test_PlainInterceptFailsWithInjectorDisabled verifies that a plain
// (sidecar) intercept is still rejected with the established
// "agent-injector is disabled" error, mirroring
// agentInjectorDisabledSuite.Test_AgentInjectorDisabled.
func (s *nodeAgentNoInjectorSuite) Test_PlainInterceptFailsWithInjectorDisabled() {
	ctx := s.Context()
	_, stderr, err := itest.Telepresence(ctx, "intercept", "echo-easy")
	s.Error(err)
	s.Contains(stderr, "agent-injector is disabled")
}

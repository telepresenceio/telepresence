package integration_test

import (
	"encoding/json/v2"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ingest"
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
	s.skipUnlessNodeAgentSupported()
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

// Test_ZUninstallReapsJobs verifies that "telepresence helm uninstall"
// deletes every node-agent Job in the manager namespace on a
// node-agent-only install, the same way it already rolls back injected
// sidecars on an injector-enabled one -- exercising the pre-delete hook's
// plain-HTTP branch and ServeNodeAgentUninstall. It is named to run last
// (testify orders test methods alphabetically within a suite) because it
// uninstalls the suite's own traffic-manager: TearDownSuite's own
// UninstallTrafficManager call runs after it and must find the release
// already gone. That is tolerated -- DeleteTrafficManager's helm-uninstall
// path is invoked with errOnFail=false, so a second uninstall of an
// already-removed release is a no-op, not a failure.
//
// The ingest is deliberately left running (no "leave") when the uninstall
// runs: ReapAllNodeAgentJobs must delete the Job regardless of any live
// client lease, unlike the per-intercept reap and the orphan sweep.
func (s *nodeAgentNoInjectorSuite) Test_ZUninstallReapsJobs() {
	ctx := s.Context()
	rq := s.Require()
	const svc = "echo-easy"

	stdout := itest.TelepresenceOk(ctx, "ingest",
		"--node-agent",
		"--mount", "false",
		"--format", "json",
		svc)
	var iInfo ingest.Info
	rq.NoError(json.Unmarshal([]byte(stdout), &iInfo))
	s.Equal(svc, iInfo.Environment["TELEPRESENCE_CONTAINER"])

	jobNames := s.nodeAgentJobNames(ctx, svc)
	rq.Len(jobNames, 1, "expected exactly one node-agent Job for %s", svc)

	// UninstallTrafficManager runs "telepresence helm uninstall" and quits
	// the client daemon once the traffic-manager deployment is gone.
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, svc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job was not reaped after helm uninstall")
}

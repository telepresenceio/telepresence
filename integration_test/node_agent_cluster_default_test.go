package integration_test

import (
	"strconv"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

// nodeAgentClusterDefaultSuite exercises the cluster-provided client default
// for node-agent mode (Helm value client.nodeAgent.enabled=true): a flagless
// intercept must be served by a node-agent, and an explicit
// --node-agent=false must override the default and fall back to sidecar
// injection. The two cases use separate workloads because an injected
// sidecar outlives its intercept and makes the workload inadmissible for
// later node-agent attachments.
type nodeAgentClusterDefaultSuite struct {
	nodeAgentBase
}

func (s *nodeAgentClusterDefaultSuite) SuiteName() string {
	return "NodeAgentClusterDefault"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &nodeAgentClusterDefaultSuite{nodeAgentBase{Suite: itest.Suite{Harness: h}, NamespacePair: h}}
	})
}

func (s *nodeAgentClusterDefaultSuite) SetupSuite() {
	s.Suite.SetupSuite()
	s.skipUnlessNodeAgentSupported()
	ctx := s.Context()
	s.reapLeftoverNodeAgentJobs(ctx)
	s.skipUnlessNodeAgentPodSecurityOK(ctx)

	// h2c probing is disabled for the same reason as the sibling node-agent
	// suites: the echo-server image answers HTTP/2 prior-knowledge probes,
	// and the local echo helpers are HTTP/1.1-only.
	s.TelepresenceHelmInstallOK(ctx, false,
		"--set", "nodeAgent.enabled=true",
		"--set", "client.nodeAgent.enabled=true",
		"--set", "agent.enableH2cProbing=false")
	itest.ApplyEchoService(ctx, "echo-default", s.AppNamespace(), 80)
	itest.ApplyEchoService(ctx, "echo-optout", s.AppNamespace(), 80)
	s.TelepresenceConnect(ctx)
}

func (s *nodeAgentClusterDefaultSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	s.DeleteSvcAndWorkload(ctx, "deploy", "echo-default")
	s.DeleteSvcAndWorkload(ctx, "deploy", "echo-optout")
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
}

// Test_ClusterDefaultSelectsNodeAgent verifies that with
// client.nodeAgent.enabled=true in the Helm chart, an intercept that never
// mentions node-agent mode is served by a node-agent: a Job appears, the
// workload's pod is left untouched, and the Job is reaped on detach.
func (s *nodeAgentClusterDefaultSuite) Test_ClusterDefaultSelectsNodeAgent() {
	const svc = "echo-default"
	ctx := s.Context()
	rq := s.Require()

	origPods := itest.RunningPods(ctx, svc, s.AppNamespace())
	rq.NotEmpty(origPods)

	port, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer cancel()

	itest.TelepresenceOk(ctx, "intercept",
		"--port", strconv.Itoa(port)+":80",
		"--mount", "false",
		svc)
	mustDetach := true
	defer func() {
		if mustDetach {
			itest.TelepresenceOk(ctx, "detach", svc)
		}
	}()

	itest.PingInterceptedEchoServer(ctx, svc, "80")

	rq.Len(s.nodeAgentJobNames(ctx, svc), 1,
		"expected a node-agent Job when client.nodeAgent.enabled=true and --node-agent is not passed")
	s.assertNoAgentInjected(ctx, svc, origPods)

	itest.TelepresenceOk(ctx, "detach", svc)
	mustDetach = false

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, svc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job was not reaped after detach")
}

// Test_FlagOverridesClusterDefault verifies that an explicit
// --node-agent=false wins over the cluster-provided default: the intercept
// falls back to sidecar injection, so a traffic-agent container appears in
// the workload's pod and no node-agent Job is created.
func (s *nodeAgentClusterDefaultSuite) Test_FlagOverridesClusterDefault() {
	const svc = "echo-optout"
	ctx := s.Context()
	rq := s.Require()

	port, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer cancel()

	itest.TelepresenceOk(ctx, "intercept",
		"--node-agent=false",
		"--port", strconv.Itoa(port)+":80",
		"--mount", "false",
		svc)
	mustDetach := true
	defer func() {
		if mustDetach {
			itest.TelepresenceOk(ctx, "detach", svc)
		}
	}()

	itest.PingInterceptedEchoServer(ctx, svc, "80")

	s.Empty(s.nodeAgentJobNames(ctx, svc),
		"no node-agent Job may be created when --node-agent=false is given")
	rq.NotEmpty(itest.RunningPodsWithAgents(ctx, svc, s.AppNamespace()),
		"expected a traffic-agent sidecar to be injected when --node-agent=false overrides the cluster default")

	itest.TelepresenceOk(ctx, "detach", svc)
	mustDetach = false
}

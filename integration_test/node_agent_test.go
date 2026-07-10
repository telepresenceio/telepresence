package integration_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ingest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
)

// nodeAgentBase holds the setup and assertions shared by the node-agent
// integration suites in this package: nodeAgentSuite (agent-injector
// enabled) and nodeAgentNoInjectorSuite (agent-injector disabled, see
// node_agent_no_injector_test.go). Both install their own traffic-manager
// with nodeAgent.enabled=true and exercise node-agent intercepts and
// ingests against the same echo-easy workload.
type nodeAgentBase struct {
	itest.Suite
	itest.NamespacePair
}

// probeNodeAgentPodSecurity performs a server-side dry-run create of a
// minimal privileged pod in the manager namespace to detect whether that
// namespace's Pod Security admission would allow the node-agent Job's pod
// (which requires hostPID and added Linux capabilities). No object is
// actually created.
func (s *nodeAgentBase) probeNodeAgentPodSecurity(ctx context.Context) error {
	podYAML := fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: node-agent-psa-probe
  namespace: %s
spec:
  hostPID: true
  restartPolicy: Never
  containers:
    - name: probe
      image: registry.k8s.io/pause:3.9
      securityContext:
        privileged: true
`, s.ManagerNamespace())
	file := filepath.Join(s.T().TempDir(), "node-agent-psa-probe.yaml")
	if err := os.WriteFile(file, []byte(podYAML), 0o644); err != nil {
		return err
	}
	_, err := itest.KubectlOut(ctx, s.ManagerNamespace(), "apply", "--dry-run=server", "-f", file)
	return err
}

// skipUnlessNodeAgentPodSecurityOK skips the current suite unless the
// manager namespace admits privileged node-agent Job pods.
func (s *nodeAgentBase) skipUnlessNodeAgentPodSecurityOK(ctx context.Context) {
	if err := s.probeNodeAgentPodSecurity(ctx); err != nil {
		if strings.Contains(err.Error(), "PodSecurity") {
			s.T().Skip("manager namespace does not admit privileged node-agent Jobs: " + err.Error())
		}
		s.Require().NoError(err)
	}
}

// nodeAgentJobNames returns the names of node-agent Jobs in the manager
// namespace for the given workload.
func (s *nodeAgentBase) nodeAgentJobNames(ctx context.Context, svc string) []string {
	selector := fmt.Sprintf("app=traffic-node-agent,telepresence.io/agentName=%s,telepresence.io/workloadNamespace=%s", svc, s.AppNamespace())
	out, err := itest.KubectlOut(ctx, s.ManagerNamespace(), "get", "jobs", "-l", selector, "-o", "jsonpath={.items[*].metadata.name}")
	s.Require().NoError(err)
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	return strings.Fields(out)
}

// reapLeftoverNodeAgentJobs deletes any node-agent Jobs already present in
// the manager namespace before this suite installs its own traffic-manager.
// Cross-suite hygiene: nodeAgentSuite and nodeAgentNoInjectorSuite share one
// manager namespace per namespace-pair group (see AddNamespacePairSuite("",
// ...) in both suites), and a previous suite's failed test can orphan Jobs
// -- the manager that would otherwise reap them may already be uninstalled
// by the time this suite starts. Names are listed by label and deleted one
// at a time by name, since a cluster's admission policy may reject a
// label-selector bulk delete.
func (s *nodeAgentBase) reapLeftoverNodeAgentJobs(ctx context.Context) {
	out, err := itest.KubectlOut(ctx, s.ManagerNamespace(), "get", "jobs", "-l", "app=traffic-node-agent", "-o", "jsonpath={.items[*].metadata.name}")
	s.Require().NoError(err)
	for _, name := range strings.Fields(strings.TrimSpace(out)) {
		s.Require().NoError(itest.Kubectl(ctx, s.ManagerNamespace(), "delete", "job", name, "--ignore-not-found"))
	}
}

// assertNoAgentInjected asserts that the given workload's pod is untouched
// by the agent-injector: same name and UID as origPod, no injected
// traffic-agent container.
func (s *nodeAgentBase) assertNoAgentInjected(ctx context.Context, svc string, origPod core.Pod) {
	rq := s.Require()
	newPods := itest.RunningPods(ctx, svc, s.AppNamespace())
	rq.Len(newPods, 1)
	s.Equal(origPod.Name, newPods[0].Name)
	s.Equal(origPod.UID, newPods[0].UID)
	for _, c := range newPods[0].Spec.Containers {
		s.NotEqual("traffic-agent", c.Name)
	}
}

// assertNodeAgentIntercept runs a node-agent intercept against svc,
// verifies parity with a regular intercept (traffic reaches the local
// process) while confirming the target workload's pod is left untouched
// (no restart, no injected traffic-agent container) and a node-agent Job
// appears in the manager namespace, then leaves the intercept and asserts
// the Job is reaped.
func (s *nodeAgentBase) assertNodeAgentIntercept(svc string) {
	ctx := s.Context()
	rq := s.Require()

	origPods := itest.RunningPods(ctx, svc, s.AppNamespace())
	rq.Len(origPods, 1, "expected exactly one running %s pod before intercepting", svc)
	origPod := origPods[0]

	port, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer cancel()

	// --mount=false avoids a dependency on FUSE (unavailable/unstable on some
	// CI runners); the assertions below only need the environment and the
	// intercepted traffic, not a mount.
	stdout := itest.TelepresenceOk(ctx, "intercept",
		"--node-agent",
		"--port", strconv.Itoa(port),
		"--detailed-output",
		"--format", "json",
		"--mount", "false",
		svc)
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(ctx, "leave", svc)
		}
	}()

	var iInfo intercept.Info
	rq.NoError(json.Unmarshal([]byte(stdout), &iInfo))
	s.Equal("ACTIVE", iInfo.Disposition)
	s.Equal("Deployment", iInfo.WorkloadKind)
	s.Equal(svc, iInfo.Environment["TELEPRESENCE_CONTAINER"])

	// Traffic sent to the service must reach the local echo server.
	itest.PingInterceptedEchoServer(ctx, svc, "80")

	// A node-agent Job must exist for this workload.
	jobNames := s.nodeAgentJobNames(ctx, svc)
	rq.Len(jobNames, 1, "expected exactly one node-agent Job for %s", svc)

	s.assertNoAgentInjected(ctx, svc, origPod)

	itest.TelepresenceOk(ctx, "leave", svc)
	mustLeave = false

	// The node-agent Job is reaped asynchronously (background deletion).
	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, svc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job was not reaped after leaving the intercept")
}

// assertNodeAgentIngest runs a node-agent ingest against svc, verifies the
// environment is sourced from the target while the workload's pod is left
// untouched and a node-agent Job appears in the manager namespace, then
// leaves the ingest and asserts the Job is reaped.
func (s *nodeAgentBase) assertNodeAgentIngest(svc string) {
	ctx := s.Context()
	rq := s.Require()

	origPods := itest.RunningPods(ctx, svc, s.AppNamespace())
	rq.Len(origPods, 1, "expected exactly one running %s pod before ingesting", svc)
	origPod := origPods[0]

	// --mount=false avoids a dependency on FUSE (unavailable/unstable on some
	// CI runners); the assertions below only need the environment, not a
	// mount.
	stdout := itest.TelepresenceOk(ctx, "ingest",
		"--node-agent",
		"--mount", "false",
		"--format", "json",
		svc)
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(ctx, "leave", svc)
		}
	}()

	var iInfo ingest.Info
	rq.NoError(json.Unmarshal([]byte(stdout), &iInfo))
	s.Equal("Deployment", iInfo.WorkloadKind)
	s.Equal(svc, iInfo.Environment["TELEPRESENCE_CONTAINER"])

	// A node-agent Job must exist for this workload.
	jobNames := s.nodeAgentJobNames(ctx, svc)
	rq.Len(jobNames, 1, "expected exactly one node-agent Job for %s", svc)

	s.assertNoAgentInjected(ctx, svc, origPod)

	itest.TelepresenceOk(ctx, "leave", svc)
	mustLeave = false

	// ReleaseAgent reaps the Job when the last ingest of the workload ends,
	// without waiting for the client to disconnect.
	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, svc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job was not reaped after leaving the ingest")
}

// nodeAgentSuite exercises "telepresence intercept --node-agent". With
// nodeAgent.enabled=true on the Helm chart, the traffic-manager serves such an
// intercept by creating a privileged Job in its own namespace instead of
// injecting a sidecar into the target workload. The Job's pod enters the
// target pod's namespaces; the target workload itself is never mutated or
// restarted.
//
// Node-agent Jobs require a privileged Pod Security posture in the
// traffic-manager's namespace (hostPID, added capabilities). SetupSuite
// initializes the harness (creating namespaces), then probes for that posture
// with a server-side dry-run, and skips the whole suite if the namespace does
// not admit it.
type nodeAgentSuite struct {
	nodeAgentBase
}

func (s *nodeAgentSuite) SuiteName() string {
	return "NodeAgent"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &nodeAgentSuite{nodeAgentBase{Suite: itest.Suite{Harness: h}, NamespacePair: h}}
	})
}

func (s *nodeAgentSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	s.reapLeftoverNodeAgentJobs(ctx)
	s.skipUnlessNodeAgentPodSecurityOK(ctx)

	// h2c probing is disabled because the echo-easy image answers HTTP/2
	// prior-knowledge probes, which makes the agent speak h2c on the
	// http-filtered delivery legs; the local echo helper is HTTP/1.1-only.
	// Protocol parity for h2c applications has its own coverage
	// (h2c_intercept_test.go); the tests in this suite target engagement
	// and filter routing.
	s.TelepresenceHelmInstallOK(ctx, false, "--set", "nodeAgent.enabled=true", "--set", "agent.enableH2cProbing=false")
	s.ApplyApp(ctx, "echo-easy", "deploy/echo-easy")
	s.TelepresenceConnect(ctx)
}

func (s *nodeAgentSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
}

// Test_NodeAgentIntercept verifies that a node-agent intercept behaves like a
// regular intercept from the client's point of view (traffic reaches the
// local process), while the target workload's pod is left untouched (no
// restart, no injected traffic-agent container) and a node-agent Job appears
// in the manager namespace and is reaped when the intercept is left.
func (s *nodeAgentSuite) Test_NodeAgentIntercept() {
	s.assertNodeAgentIntercept("echo-easy")
}

// Test_NodeAgentIngest verifies that "telepresence ingest --node-agent"
// serves the ingest from a node-agent Job instead of an injected sidecar:
// the environment is sourced from the target workload, the workload's pod
// is left untouched, exactly one node-agent Job exists while the ingest is
// active, and the Job is reaped promptly (via the manager's ReleaseAgent
// path) once the ingest is left -- without waiting for disconnect.
func (s *nodeAgentSuite) Test_NodeAgentIngest() {
	s.assertNodeAgentIngest("echo-easy")
}

// Test_NodeAgentWiretap verifies that "telepresence wiretap --node-agent"
// taps traffic via a node-agent Job while the target workload's pod keeps
// serving the original traffic unmodified (pass-through) and is never
// mutated (no injected traffic-agent container). The node-agent Job is
// reaped once the wiretap is left.
func (s *nodeAgentSuite) Test_NodeAgentWiretap() {
	const svc = "echo-easy"
	ctx := s.Context()
	rq := s.Require()

	origPods := itest.RunningPods(ctx, svc, s.AppNamespace())
	rq.Len(origPods, 1, "expected exactly one running %s pod before wiretapping", svc)
	origPod := origPods[0]

	tapHits := make(chan struct{}, 100)
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp", ":0")
	rq.NoError(err, "failed to listen on localhost")
	tapPort := l.Addr().(*net.TCPAddr).Port
	tapSrv := &http.Server{
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			select {
			case tapHits <- struct{}{}:
			default:
			}
		}),
	}
	go func() {
		_ = tapSrv.Serve(l)
	}()
	defer func() {
		sCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = tapSrv.Shutdown(sCtx)
	}()

	// --mount=false avoids a dependency on FUSE (unavailable/unstable on some
	// CI runners); the assertions below only need the tapped and pass-through
	// traffic, not a mount.
	stdout := itest.TelepresenceOk(ctx, "wiretap",
		"--node-agent",
		"--workload", svc,
		"--mount=false",
		"--port", fmt.Sprintf("%d:80", tapPort),
		"wt1")
	s.Contains(stdout, "Using Deployment "+svc)
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(ctx, "leave", "wt1")
		}
	}()

	// A node-agent Job must exist for this workload, and the target pod must
	// remain unmutated.
	jobNames := s.nodeAgentJobNames(ctx, svc)
	rq.Len(jobNames, 1, "expected exactly one node-agent Job for %s", svc)
	s.assertNoAgentInjected(ctx, svc, origPod)

	// The application must keep serving the original traffic while the tap
	// receives a copy of it.
	// First, verify the application keeps serving through the pass-through dial.
	rq.Eventually(func() bool {
		so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", svc)
		if err != nil {
			return false
		}
		return strings.Contains(so, "Request served by "+origPod.Name)
	}, 30*time.Second, 3*time.Second, "application did not keep serving through the wiretap pass-through")

	// The tap must receive a copy of the traffic. Since tap delivery is
	// asynchronous, we need to issue a curl in each iteration and then check
	// if a tap hit arrives.
	rq.Eventually(func() bool {
		_, _ = itest.Output(ctx, "curl", "--silent", "--max-time", "2", svc) // curl output is ignored; we only care about tap delivery
		select {
		case <-tapHits:
			return true
		default:
			return false
		}
	}, 30*time.Second, 3*time.Second, "wiretap tap did not receive a copy of the traffic")

	itest.TelepresenceOk(ctx, "leave", "wt1")
	mustLeave = false

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, svc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job was not reaped after leaving the wiretap")
}

// Test_NodeAgentConfigDefault verifies that the client config's
// nodeAgent.enabled default is honored by "telepresence intercept" when
// --node-agent is not passed: a node-agent Job appears even though the
// command line never mentions node-agent mode, and it is reaped once the
// intercept is left.
func (s *nodeAgentSuite) Test_NodeAgentConfigDefault() {
	const svc = "echo-easy"
	rq := s.Require()

	cfgCtx := itest.WithConfig(s.Context(), func(cfg client.Config) {
		cfg.NodeAgent().Enabled = true
	})
	s.TelepresenceConnect(cfgCtx)
	defer func() {
		itest.TelepresenceQuitOk(cfgCtx)
		// Restore the suite's shared connection (default config, so
		// --node-agent again defaults to disabled) for the remaining tests.
		s.TelepresenceConnect(s.Context())
	}()

	port, cancel := itest.StartLocalHttpEchoServer(cfgCtx, svc)
	defer cancel()

	itest.TelepresenceOk(cfgCtx, "intercept",
		"--port", strconv.Itoa(port),
		"--mount", "false",
		svc)
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(cfgCtx, "leave", svc)
		}
	}()

	itest.PingInterceptedEchoServer(cfgCtx, svc, "80")

	jobNames := s.nodeAgentJobNames(cfgCtx, svc)
	rq.Len(jobNames, 1, "expected a node-agent Job when config.yml sets nodeAgent.enabled=true and --node-agent is not passed")

	itest.TelepresenceOk(cfgCtx, "leave", svc)
	mustLeave = false

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(cfgCtx, svc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job was not reaped after leaving the intercept")
}

// Test_NodeAgentHTTPFilteredIntercept verifies that "telepresence intercept
// --node-agent --http-header ..." engages the node-agent's HTTP-filtered
// path: the node-agent Job switches to its HTTP listener and reverse-proxy
// transport, so requests carrying the configured header are routed to the
// local process while requests without it keep reaching the real
// application through the node-agent's pass-through dial (which uses the
// target workload's network namespace). The target pod is left untouched
// and the node-agent Job is reaped once the intercept is left.
func (s *nodeAgentSuite) Test_NodeAgentHTTPFilteredIntercept() {
	const svc = "echo-easy"
	ctx := s.Context()
	rq := s.Require()

	origPods := itest.RunningPods(ctx, svc, s.AppNamespace())
	rq.Len(origPods, 1, "expected exactly one running %s pod before intercepting", svc)
	origPod := origPods[0]

	port, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer cancel()

	header := "x-telepresence-test=node-agent-http-filter-intercept"

	// --mount=false avoids a dependency on FUSE (unavailable/unstable on some
	// CI runners); the assertions below only need the header-routed and
	// pass-through traffic, not a mount.
	stdout := itest.TelepresenceOk(ctx, "intercept",
		"--node-agent",
		"--port", strconv.Itoa(port),
		"--mount", "false",
		"--http-header", header,
		svc)
	s.Contains(stdout, "Using Deployment")
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(ctx, "leave", svc)
		}
	}()

	// A request carrying the filter header must reach the local echo server.
	itest.PingInterceptedEchoServerAndExpect(ctx, svc, "80", svc+" from intercept at /", header)

	// A request without the header must keep reaching the real application
	// through the node-agent's pass-through dial.
	rq.Eventually(func() bool {
		so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", svc)
		if err != nil {
			return false
		}
		return strings.Contains(so, "Request served by "+origPod.Name)
	}, 30*time.Second, 3*time.Second, "unfiltered request did not reach the real application through the node-agent pass-through")

	// A node-agent Job must exist for this workload.
	jobNames := s.nodeAgentJobNames(ctx, svc)
	rq.Len(jobNames, 1, "expected exactly one node-agent Job for %s", svc)

	s.assertNoAgentInjected(ctx, svc, origPod)

	itest.TelepresenceOk(ctx, "leave", svc)
	mustLeave = false

	// The node-agent Job is reaped asynchronously (background deletion).
	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, svc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job was not reaped after leaving the intercept")
}

// Test_NodeAgentHTTPFilteredWiretap verifies that "telepresence wiretap
// --node-agent --http-header ..." taps only the traffic that matches the
// header filter, via the node-agent's HTTP listener and reverse-proxy
// transport: a request carrying the header keeps reaching the real
// application (a wiretap never steals traffic) and a copy of it arrives at
// the local tap, while a request without the header also keeps reaching the
// real application but is never copied to the tap. The target pod is left
// untouched and the node-agent Job is reaped once the wiretap is left.
func (s *nodeAgentSuite) Test_NodeAgentHTTPFilteredWiretap() {
	const svc = "echo-easy"
	ctx := s.Context()
	rq := s.Require()

	origPods := itest.RunningPods(ctx, svc, s.AppNamespace())
	rq.Len(origPods, 1, "expected exactly one running %s pod before wiretapping", svc)
	origPod := origPods[0]

	tapHits := make(chan struct{}, 100)
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp", ":0")
	rq.NoError(err, "failed to listen on localhost")
	tapPort := l.Addr().(*net.TCPAddr).Port
	tapSrv := &http.Server{
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			select {
			case tapHits <- struct{}{}:
			default:
			}
		}),
	}
	go func() {
		_ = tapSrv.Serve(l)
	}()
	defer func() {
		sCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_ = tapSrv.Shutdown(sCtx)
	}()

	const headerName = "x-telepresence-test"
	const headerValue = "node-agent-http-filter-wiretap"
	header := headerName + "=" + headerValue

	// --mount=false avoids a dependency on FUSE (unavailable/unstable on some
	// CI runners); the assertions below only need the tapped and pass-through
	// traffic, not a mount.
	stdout := itest.TelepresenceOk(ctx, "wiretap",
		"--node-agent",
		"--workload", svc,
		"--mount=false",
		"--port", fmt.Sprintf("%d:80", tapPort),
		"--http-header", header,
		"wt2")
	s.Contains(stdout, "Using Deployment "+svc)
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(ctx, "leave", "wt2")
		}
	}()

	// A node-agent Job must exist for this workload, and the target pod must
	// remain unmutated.
	jobNames := s.nodeAgentJobNames(ctx, svc)
	rq.Len(jobNames, 1, "expected exactly one node-agent Job for %s", svc)
	s.assertNoAgentInjected(ctx, svc, origPod)

	curlWithHeader := func() (string, error) {
		return itest.Output(ctx, "curl", "--silent", "--max-time", "2", "-H", headerName+": "+headerValue, svc)
	}
	curlNoHeader := func() (string, error) {
		return itest.Output(ctx, "curl", "--silent", "--max-time", "2", svc)
	}

	// A request carrying the filter header must still reach the real
	// application: a wiretap never steals traffic, it only copies it.
	rq.Eventually(func() bool {
		so, err := curlWithHeader()
		if err != nil {
			return false
		}
		return strings.Contains(so, "Request served by "+origPod.Name)
	}, 30*time.Second, 3*time.Second, "application did not keep serving header-matching traffic through the wiretap pass-through")

	// The tap must receive a copy of the header-matching traffic. Since tap
	// delivery is asynchronous, issue a request in each iteration and then
	// check if a tap hit arrives.
	rq.Eventually(func() bool {
		_, _ = curlWithHeader() // curl output is ignored; we only care about tap delivery
		select {
		case <-tapHits:
			return true
		default:
			return false
		}
	}, 30*time.Second, 3*time.Second, "wiretap tap did not receive a copy of the header-matching traffic")

	// Drain any straggler hits left over from the positive phase above --
	// tap delivery is asynchronous, so a copy of the last matched request
	// could still be in flight -- before checking the negative case below.
	drainDeadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case <-tapHits:
		case <-drainDeadline:
			break drain
		}
	}

	// A request without the header must keep reaching the real application.
	rq.Eventually(func() bool {
		so, err := curlNoHeader()
		if err != nil {
			return false
		}
		return strings.Contains(so, "Request served by "+origPod.Name)
	}, 30*time.Second, 3*time.Second, "application did not keep serving non-matching traffic through the wiretap pass-through")

	// ...but it must never reach the tap. Issue several non-matching
	// requests, then assert no tap hit shows up within a modest window.
	for i := 0; i < 5; i++ {
		_, _ = curlNoHeader() // curl output is ignored; we only care about tap delivery
	}
	select {
	case <-tapHits:
		s.Fail("wiretap tap received a copy of traffic that did not match the header filter")
	case <-time.After(2 * time.Second):
	}

	itest.TelepresenceOk(ctx, "leave", "wt2")
	mustLeave = false

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, svc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job was not reaped after leaving the wiretap")
}

// Test_NodeAgentRejectsReplace verifies that the traffic-manager refuses a
// node-agent intercept that also requests --replace: node-agent mode never
// runs the sidecar machinery that --replace depends on, so the target
// container would keep running and the replace would be silently ignored.
// The combination is not rejected client-side; it is rejected by the
// traffic-manager's PrepareIntercept.
func (s *nodeAgentSuite) Test_NodeAgentRejectsReplace() {
	const svc = "echo-easy"
	ctx := s.Context()

	port, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer cancel()

	_, stderr, err := itest.Telepresence(ctx, "intercept",
		"--node-agent",
		"--replace",
		"--port", strconv.Itoa(port),
		"--mount", "false",
		svc)
	s.Error(err)
	s.Contains(stderr, "node-agent mode does not support --replace")
}

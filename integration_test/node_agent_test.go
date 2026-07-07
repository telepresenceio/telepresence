package integration_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
)

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
	itest.Suite
	itest.NamespacePair
}

func (s *nodeAgentSuite) SuiteName() string {
	return "NodeAgent"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &nodeAgentSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *nodeAgentSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	if err := s.probeNodeAgentPodSecurity(ctx); err != nil {
		if strings.Contains(err.Error(), "PodSecurity") {
			s.T().Skip("manager namespace does not admit privileged node-agent Jobs: " + err.Error())
		}
		s.Require().NoError(err)
	}

	s.TelepresenceHelmInstallOK(ctx, false, "--set", "nodeAgent.enabled=true")
	s.ApplyApp(ctx, "echo-easy", "deploy/echo-easy")
	s.TelepresenceConnect(ctx)
}

func (s *nodeAgentSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
}

// probeNodeAgentPodSecurity performs a server-side dry-run create of a
// minimal privileged pod in the manager namespace to detect whether that
// namespace's Pod Security admission would allow the node-agent Job's pod
// (which requires hostPID and added Linux capabilities). No object is
// actually created.
func (s *nodeAgentSuite) probeNodeAgentPodSecurity(ctx context.Context) error {
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

// nodeAgentJobNames returns the names of node-agent Jobs in the manager
// namespace for the given workload.
func (s *nodeAgentSuite) nodeAgentJobNames(ctx context.Context, svc string) []string {
	selector := fmt.Sprintf("app=traffic-node-agent,telepresence.io/agentName=%s,telepresence.io/workloadNamespace=%s", svc, s.AppNamespace())
	out, err := itest.KubectlOut(ctx, s.ManagerNamespace(), "get", "jobs", "-l", selector, "-o", "jsonpath={.items[*].metadata.name}")
	s.Require().NoError(err)
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	return strings.Fields(out)
}

// Test_NodeAgentIntercept verifies that a node-agent intercept behaves like a
// regular intercept from the client's point of view (traffic reaches the
// local process), while the target workload's pod is left untouched (no
// restart, no injected traffic-agent container) and a node-agent Job appears
// in the manager namespace and is reaped when the intercept is left.
func (s *nodeAgentSuite) Test_NodeAgentIntercept() {
	const svc = "echo-easy"
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

	// The target workload's pod must be untouched: same name and UID as
	// before the intercept started, and no injected traffic-agent container.
	newPods := itest.RunningPods(ctx, svc, s.AppNamespace())
	rq.Len(newPods, 1)
	s.Equal(origPod.Name, newPods[0].Name)
	s.Equal(origPod.UID, newPods[0].UID)
	for _, c := range newPods[0].Spec.Containers {
		s.NotEqual("traffic-agent", c.Name)
	}

	itest.TelepresenceOk(ctx, "leave", svc)
	mustLeave = false

	// The node-agent Job is reaped asynchronously (background deletion).
	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, svc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job was not reaped after leaving the intercept")
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

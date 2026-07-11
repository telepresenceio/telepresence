package integration_test

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

// nodeAgentMultiSvc is the workload used by every test in this suite: 4
// replicas, one HTTP port, no h2c (see SetupSuite).
const nodeAgentMultiSvc = "echo-4replicas"

// nodeAgentMultiSuite exercises the two extensions built on top of the
// single-pod, single-intercept node-agent mode covered by nodeAgentSuite:
// attaching to every replica of a workload (with a reconciler that follows
// replica churn) and sharing one workload's Job set across several
// concurrent node-agent intercepts. It reuses nodeAgentBase's setup and
// assertion helpers and installs its own traffic-manager against
// deploy/echo-4replicas.
type nodeAgentMultiSuite struct {
	nodeAgentBase
}

func (s *nodeAgentMultiSuite) SuiteName() string {
	return "NodeAgentMulti"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &nodeAgentMultiSuite{nodeAgentBase{Suite: itest.Suite{Harness: h}, NamespacePair: h}}
	})
}

func (s *nodeAgentMultiSuite) SetupSuite() {
	s.Suite.SetupSuite()
	s.skipUnlessNodeAgentSupported()
	ctx := s.Context()
	s.reapLeftoverNodeAgentJobs(ctx)
	s.skipUnlessNodeAgentPodSecurityOK(ctx)

	// h2c probing is disabled for the same reason as nodeAgentSuite: the
	// upstream echo-server image answers HTTP/2 prior-knowledge probes, which
	// would make the agent speak h2c on the http-filtered delivery legs, and
	// this suite's local echo helpers are HTTP/1.1-only.
	s.TelepresenceHelmInstallOK(ctx, false, "--set", "nodeAgent.enabled=true", "--set", "agent.enableH2cProbing=false")
	s.ApplyApp(ctx, nodeAgentMultiSvc, "deploy/"+nodeAgentMultiSvc)
	s.Require().Eventually(func() bool {
		return len(itest.RunningPodNames(ctx, nodeAgentMultiSvc, s.AppNamespace())) == 4
	}, itest.PodCreateTimeout(ctx), 6*time.Second, "waiting for 4 running pods")
	s.TelepresenceConnect(ctx)
}

func (s *nodeAgentMultiSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	s.DeleteSvcAndWorkload(ctx, "deploy", nodeAgentMultiSvc)
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
}

// nodeAgentMultiFireBurst fires n concurrent HTTP GETs against svc with
// keep-alives disabled, so each request opens a fresh connection and is
// independently load-balanced across the workload's replicas (the pattern
// from multi_replica_intercept_test.go). header, if non-empty, is a
// "key=value" pair added to every request. A small random jitter staggers
// the requests so they don't all hit the service in lock-step. The returned
// slice holds one response body per request, in no particular order; a
// request that errors is recorded as "ERROR: <message>", which can never
// match a legitimate expected body.
func nodeAgentMultiFireBurst(ctx context.Context, svc, header string, n int) []string {
	hc := http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	bodies := make([]string, n)
	done := make(chan struct{}, n)
	for i := range n {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			time.Sleep(time.Duration(rand.IntN(500)) * time.Millisecond)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+svc, nil)
			if err != nil {
				clog.Infof(ctx, "request %d: build error: %v", i, err)
				bodies[i] = "ERROR: " + err.Error()
				return
			}
			if header != "" {
				kv := strings.SplitN(header, "=", 2)
				req.Header.Set(kv[0], kv[1])
			}
			resp, err := hc.Do(req)
			if err != nil {
				clog.Infof(ctx, "request %d: error: %v", i, err)
				bodies[i] = "ERROR: " + err.Error()
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			bodies[i] = string(body)
		}(i)
	}
	for range n {
		<-done
	}
	return bodies
}

// nodeAgentMultiCountMismatches returns how many of bodies are not equal to
// expect.
func nodeAgentMultiCountMismatches(bodies []string, expect string) int {
	n := 0
	for _, b := range bodies {
		if b != expect {
			n++
		}
	}
	return n
}

// nodeAgentMultiPollServedBy issues repeated single-shot curls against svc
// (each its own connection, so independently load-balanced) and collects the
// distinct pod names reported in "Request served by <pod>" response bodies,
// until minDistinct distinct names have been seen or timeout elapses.
func nodeAgentMultiPollServedBy(ctx context.Context, svc string, minDistinct int, timeout time.Duration) map[string]struct{} {
	const marker = "Request served by "
	served := map[string]struct{}{}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) && len(served) < minDistinct {
		so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", svc)
		if err == nil {
			if idx := strings.Index(so, marker); idx >= 0 {
				served[strings.TrimSpace(so[idx+len(marker):])] = struct{}{}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return served
}

// Test_NodeAgentGlobalInterceptAllReplicas verifies that a global (unfiltered)
// node-agent intercept attaches to every replica of the workload -- so
// traffic the service load-balances to any of them is intercepted -- and
// that the manager's pod-set reconciler follows replica churn: a Job appears
// for a newly scaled-up replica once it is Running & Ready, and a removed
// replica's Job is reaped while the intercept stays ACTIVE. The workload's
// pods themselves are never mutated, and every Job is reaped once the
// intercept is detached.
func (s *nodeAgentMultiSuite) Test_NodeAgentGlobalInterceptAllReplicas() {
	const name = "node-agent-multi-global"
	ctx := s.Context()
	rq := s.Require()

	origPods := itest.RunningPods(ctx, nodeAgentMultiSvc, s.AppNamespace())
	rq.Len(origPods, 4, "expected 4 running %s pods before intercepting", nodeAgentMultiSvc)
	origNames := itest.RunningPodNames(ctx, nodeAgentMultiSvc, s.AppNamespace())

	port, cancel := itest.StartLocalHttpEchoServer(ctx, nodeAgentMultiSvc)
	defer cancel()

	// --mount=false avoids a dependency on FUSE (unavailable/unstable on some
	// CI runners); the assertions below only need the intercepted traffic,
	// not a mount.
	itest.TelepresenceOk(ctx, "intercept", name,
		"--node-agent",
		"--workload", nodeAgentMultiSvc,
		"--port", strconv.Itoa(port),
		"--mount", "false")
	mustDetach := true
	defer func() {
		if mustDetach {
			itest.TelepresenceOk(ctx, "detach", name)
		}
	}()
	s.CapturePodLogs(ctx, nodeAgentMultiSvc, "", s.AppNamespace())

	// One Job per running pod, and the Job set matches the pod set exactly.
	sortedNames := append([]string(nil), origNames...)
	sort.Strings(sortedNames)
	rq.Eventually(func() bool {
		return slices.Equal(sortedNames, s.nodeAgentJobTargetPods(ctx, nodeAgentMultiSvc))
	}, 60*time.Second, 3*time.Second, "expected one node-agent Job per running pod")

	s.assertNoAgentInjected(ctx, nodeAgentMultiSvc, origPods)

	// Allow a short settling period for all 4 agents to register with the
	// intercept before the burst (the multi_replica_intercept_test.go
	// pattern).
	time.Sleep(5 * time.Second)

	expect := nodeAgentMultiSvc + " from intercept at /"
	bodies := nodeAgentMultiFireBurst(ctx, nodeAgentMultiSvc, "", 100)
	rq.Zero(nodeAgentMultiCountMismatches(bodies, expect), "not every request was intercepted across all 4 replicas")

	// Churn: scale up. A 5th Job must appear once the new pod is Running &
	// Ready -- the reconciler debounces at ~1s, but pod startup dominates the
	// timeout budget.
	rq.NoError(s.Kubectl(ctx, "scale", "deploy", nodeAgentMultiSvc, "--replicas", "5"))
	rq.Eventually(func() bool {
		return len(itest.RunningPodNames(ctx, nodeAgentMultiSvc, s.AppNamespace())) == 5
	}, itest.PodCreateTimeout(ctx), 3*time.Second, "waiting for the 5th pod to become running")
	rq.Eventually(func() bool {
		return len(s.nodeAgentJobTargetPods(ctx, nodeAgentMultiSvc)) == 5
	}, 90*time.Second, 3*time.Second, "expected a 5th node-agent Job after scaling up")
	rq.ElementsMatch(itest.RunningPodNames(ctx, nodeAgentMultiSvc, s.AppNamespace()), s.nodeAgentJobTargetPods(ctx, nodeAgentMultiSvc))

	time.Sleep(5 * time.Second) // settle: the new pod's agent must register
	bodies = nodeAgentMultiFireBurst(ctx, nodeAgentMultiSvc, "", 100)
	rq.Zero(nodeAgentMultiCountMismatches(bodies, expect), "not every request was intercepted after scaling to 5 replicas")

	// Scale back down. The removed pod's Job must be reaped while the
	// intercept stays ACTIVE.
	rq.NoError(s.Kubectl(ctx, "scale", "deploy", nodeAgentMultiSvc, "--replicas", "4"))
	rq.Eventually(func() bool {
		return len(s.nodeAgentJobTargetPods(ctx, nodeAgentMultiSvc)) == 4
	}, 90*time.Second, 3*time.Second, "expected the removed pod's node-agent Job to be reaped")

	out, _, err := itest.Telepresence(ctx, "list", "--intercepts")
	rq.NoError(err)
	s.Contains(out, "ACTIVE")

	itest.TelepresenceOk(ctx, "detach", name)
	mustDetach = false

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, nodeAgentMultiSvc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Jobs were not reaped after detaching")
}

// Test_NodeAgentHTTPFilteredInterceptAllReplicas verifies that an
// --http-header node-agent intercept filters on every replica: a burst of
// header-tagged requests must all reach the local process regardless of
// which replica the service load-balanced them to, while headerless
// requests keep reaching the real application spread across more than one
// replica -- proving the pass-through path, not just the attachment, works
// per replica.
func (s *nodeAgentMultiSuite) Test_NodeAgentHTTPFilteredInterceptAllReplicas() {
	const name = "node-agent-multi-http"
	ctx := s.Context()
	rq := s.Require()

	origPods := itest.RunningPods(ctx, nodeAgentMultiSvc, s.AppNamespace())
	rq.Len(origPods, 4, "expected 4 running %s pods before intercepting", nodeAgentMultiSvc)

	port, cancel := itest.StartLocalHttpEchoServer(ctx, nodeAgentMultiSvc)
	defer cancel()

	header := "x-telepresence-test=node-agent-multi-http-filter"

	itest.TelepresenceOk(ctx, "intercept", name,
		"--node-agent",
		"--workload", nodeAgentMultiSvc,
		"--port", strconv.Itoa(port),
		"--mount", "false",
		"--http-header", header)
	mustDetach := true
	defer func() {
		if mustDetach {
			itest.TelepresenceOk(ctx, "detach", name)
		}
	}()
	s.CapturePodLogs(ctx, nodeAgentMultiSvc, "", s.AppNamespace())

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobTargetPods(ctx, nodeAgentMultiSvc)) == 4
	}, 60*time.Second, 3*time.Second, "expected one node-agent Job per running pod")
	s.assertNoAgentInjected(ctx, nodeAgentMultiSvc, origPods)

	time.Sleep(5 * time.Second)

	// Header-tagged burst must all be intercepted, on every replica.
	expect := nodeAgentMultiSvc + " from intercept at /"
	bodies := nodeAgentMultiFireBurst(ctx, nodeAgentMultiSvc, header, 100)
	rq.Zero(nodeAgentMultiCountMismatches(bodies, expect), "not every header-tagged request was intercepted across all 4 replicas")

	// Headerless requests must keep reaching the application, and land on
	// more than one replica -- proving per-replica pass-through, not just
	// attachment.
	served := nodeAgentMultiPollServedBy(ctx, nodeAgentMultiSvc, 2, 30*time.Second)
	rq.GreaterOrEqual(len(served), 2, "headerless requests reached only one replica: %v", served)

	itest.TelepresenceOk(ctx, "detach", name)
	mustDetach = false

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, nodeAgentMultiSvc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Jobs were not reaped after detaching")
}

// Test_NodeAgentSharedJobHTTPFiltered verifies that two concurrent
// --http-header node-agent intercepts on the same workload share its Job set
// instead of duplicating it: the sorted Job-name set is identical before and
// after the second intercept starts, each header routes to its own local
// server, headerless requests still reach the application, and the Jobs
// survive the first intercept's detach but not the second's.
func (s *nodeAgentMultiSuite) Test_NodeAgentSharedJobHTTPFiltered() {
	ctx := s.Context()
	rq := s.Require()

	origPods := itest.RunningPods(ctx, nodeAgentMultiSvc, s.AppNamespace())
	rq.Len(origPods, 4, "expected 4 running %s pods before intercepting", nodeAgentMultiSvc)

	portAdam, cancelAdam := itest.StartLocalHttpEchoServer(ctx, "adam")
	defer cancelAdam()
	portBertil, cancelBertil := itest.StartLocalHttpEchoServer(ctx, "bertil")
	defer cancelBertil()

	const nameAdam = "node-agent-multi-adam"
	const nameBertil = "node-agent-multi-bertil"

	itest.TelepresenceOk(ctx, "intercept", nameAdam,
		"--node-agent",
		"--workload", nodeAgentMultiSvc,
		"--http-header", "x-user=adam",
		"--port", strconv.Itoa(portAdam),
		"--mount", "false")
	adamLive := true
	defer func() {
		if adamLive {
			itest.TelepresenceOk(ctx, "detach", nameAdam)
		}
	}()
	rq.Eventually(func() bool {
		return len(s.nodeAgentJobTargetPods(ctx, nodeAgentMultiSvc)) == 4
	}, 60*time.Second, 3*time.Second, "expected one node-agent Job per running pod")
	jobsAfterFirst := s.nodeAgentJobNames(ctx, nodeAgentMultiSvc)
	sort.Strings(jobsAfterFirst)

	itest.TelepresenceOk(ctx, "intercept", nameBertil,
		"--node-agent",
		"--workload", nodeAgentMultiSvc,
		"--http-header", "x-user=bertil",
		"--port", strconv.Itoa(portBertil),
		"--mount", "false")
	bertilLive := true
	defer func() {
		if bertilLive {
			itest.TelepresenceOk(ctx, "detach", nameBertil)
		}
	}()

	jobsAfterSecond := s.nodeAgentJobNames(ctx, nodeAgentMultiSvc)
	sort.Strings(jobsAfterSecond)
	rq.Equal(jobsAfterFirst, jobsAfterSecond, "the second intercept must share the Job set, not create new Jobs")
	s.assertNoAgentInjected(ctx, nodeAgentMultiSvc, origPods)

	// Each header must route to its own local server.
	itest.PingInterceptedEchoServerAndExpect(ctx, nodeAgentMultiSvc, "80", "adam from intercept at /", "x-user=adam")
	itest.PingInterceptedEchoServerAndExpect(ctx, nodeAgentMultiSvc, "80", "bertil from intercept at /", "x-user=bertil")

	// Headerless requests must keep reaching the real application.
	rq.Eventually(func() bool {
		so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", nodeAgentMultiSvc)
		return err == nil && strings.Contains(so, "Request served by")
	}, 30*time.Second, 3*time.Second, "headerless requests did not reach the application")

	itest.TelepresenceOk(ctx, "detach", nameAdam)
	adamLive = false

	// The Jobs must survive adam's detach, and bertil's intercept must keep
	// routing.
	rq.Len(s.nodeAgentJobNames(ctx, nodeAgentMultiSvc), 4, "Jobs must remain while the second intercept is still live")
	itest.PingInterceptedEchoServerAndExpect(ctx, nodeAgentMultiSvc, "80", "bertil from intercept at /", "x-user=bertil")

	itest.TelepresenceOk(ctx, "detach", nameBertil)
	bertilLive = false

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, nodeAgentMultiSvc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Jobs were not reaped after detaching both intercepts")
}

// Test_NodeAgentSharedJobGlobal covers global-intercept sharing and its
// conflict boundary in two phases:
//
//  1. On echo-4replicas, a second global node-agent intercept of the same
//     port must be rejected -- the standard conflict, unchanged from the
//     sidecar -- and, the regression this phase guards against, the first
//     intercept's Jobs must not be touched by the failed attempt.
//  2. On a two-port, single-replica workload, two concurrent global
//     node-agent intercepts on the two different ports must both succeed,
//     each routing to its own local server, sharing the workload's single
//     Job throughout.
func (s *nodeAgentMultiSuite) Test_NodeAgentSharedJobGlobal() {
	ctx := s.Context()
	rq := s.Require()

	// Phase 1: conflict safety.
	portFirst, cancelFirst := itest.StartLocalHttpEchoServer(ctx, "global-first")
	defer cancelFirst()

	const nameFirst = "node-agent-multi-global-first"
	itest.TelepresenceOk(ctx, "intercept", nameFirst,
		"--node-agent",
		"--workload", nodeAgentMultiSvc,
		"--port", strconv.Itoa(portFirst),
		"--mount", "false")
	firstLive := true
	defer func() {
		if firstLive {
			itest.TelepresenceOk(ctx, "detach", nameFirst)
		}
	}()

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobTargetPods(ctx, nodeAgentMultiSvc)) == 4
	}, 60*time.Second, 3*time.Second, "expected one node-agent Job per running pod")
	jobsBeforeConflict := s.nodeAgentJobNames(ctx, nodeAgentMultiSvc)
	sort.Strings(jobsBeforeConflict)

	portSecond, cancelSecond := itest.StartLocalHttpEchoServer(ctx, "global-second")
	defer cancelSecond()

	_, stderr, err := itest.Telepresence(ctx, "intercept", "node-agent-multi-global-second",
		"--node-agent",
		"--workload", nodeAgentMultiSvc,
		"--port", strconv.Itoa(portSecond),
		"--mount", "false")
	rq.Error(err, "a second global intercept of the same port must be rejected")
	s.Contains(stderr, "one intercept has no filters (intercepts all traffic)")

	// The first intercept must be unaffected: still routes, Jobs untouched.
	itest.PingInterceptedEchoServer(ctx, nodeAgentMultiSvc+"/global-first", "80")
	jobsAfterConflict := s.nodeAgentJobNames(ctx, nodeAgentMultiSvc)
	sort.Strings(jobsAfterConflict)
	rq.Equal(jobsBeforeConflict, jobsAfterConflict, "a rejected second intercept must not disturb the first's Jobs")

	itest.TelepresenceOk(ctx, "detach", nameFirst)
	firstLive = false
	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, nodeAgentMultiSvc)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Jobs were not reaped after detaching")

	// Phase 2: different-port sharing on a two-port, single-replica workload.
	const dep = "echo-double-one-unnamed"
	s.ApplyApp(ctx, dep, "deploy/"+dep)
	defer s.DeleteApp(ctx, dep)

	portA, cancelA := itest.StartLocalHttpEchoServer(ctx, "port-a")
	defer cancelA()
	portB, cancelB := itest.StartLocalHttpEchoServer(ctx, "port-b")
	defer cancelB()

	const nameA = "node-agent-multi-port-a"
	const nameB = "node-agent-multi-port-b"

	itest.TelepresenceOk(ctx, "intercept", nameA,
		"--node-agent",
		"--workload", dep,
		"--port", fmt.Sprintf("%d:8080", portA),
		"--mount", "false")
	aLive := true
	defer func() {
		if aLive {
			itest.TelepresenceOk(ctx, "detach", nameA)
		}
	}()

	itest.TelepresenceOk(ctx, "intercept", nameB,
		"--node-agent",
		"--workload", dep,
		"--port", fmt.Sprintf("%d:8081", portB),
		"--mount", "false")
	bLive := true
	defer func() {
		if bLive {
			itest.TelepresenceOk(ctx, "detach", nameB)
		}
	}()

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, dep)) == 1
	}, 60*time.Second, 3*time.Second, "expected exactly one shared node-agent Job for %s", dep)

	var pods []core.Pod
	rq.Eventually(func() bool {
		pods = itest.RunningPods(ctx, dep, s.AppNamespace())
		return len(pods) == 1
	}, 60*time.Second, 3*time.Second, "expected exactly one running %s pod", dep)
	podIP := pods[0].Status.PodIP

	itest.PingInterceptedEchoServerAndExpect(ctx, podIP, "8080", "port-a from intercept at /")
	itest.PingInterceptedEchoServerAndExpect(ctx, podIP, "8081", "port-b from intercept at /")

	itest.TelepresenceOk(ctx, "detach", nameA)
	aLive = false

	rq.Len(s.nodeAgentJobNames(ctx, dep), 1, "the shared Job must survive while the other intercept is still live")
	itest.PingInterceptedEchoServerAndExpect(ctx, podIP, "8081", "port-b from intercept at /")

	itest.TelepresenceOk(ctx, "detach", nameB)
	bLive = false

	rq.Eventually(func() bool {
		return len(s.nodeAgentJobNames(ctx, dep)) == 0
	}, 60*time.Second, 2*time.Second, "node-agent Job for %s was not reaped after detaching", dep)
}

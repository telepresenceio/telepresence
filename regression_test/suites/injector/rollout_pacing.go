package injector

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// RolloutPacing proves that the traffic-manager replaces a workload's
// agent-carrying pods within the workload's own rolling-update allowance
// when the agent configuration changes without the pod template changing.
type RolloutPacing struct {
	rt.Suite
}

func init() {
	rt.Register(&RolloutPacing{}, rt.InArea("injector"), rt.NeedsManager(managers.Default))
}

// pacingTimeout bounds the wait for every pod of the workload to be
// replaced: two batches of two pods, each needing the echo image to start
// and pass its readiness probe.
const pacingTimeout = 3 * time.Minute

// Test_AgentUpdateHonorsMaxUnavailable rolls out a four-replica Deployment
// with maxUnavailable 2 and a PodDisruptionBudget of minAvailable 2, all
// pods carrying an agent, then changes the Service port. The agent config
// records that port, so every pod's config is stale while the pod template
// is untouched, and the manager must replace the pods itself: two at a
// time, never leaving fewer than two ready, and without falling back to a
// controller-driven restart (which would bump the Deployment's generation).
func (s *RolloutPacing) Test_AgentUpdateHonorsMaxUnavailable() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	tpl := workloads.EchoReplicas("rollout-pacing", 4)
	tpl.MaxUnavailable = 2
	tpl.MinAvailable = 2
	tpl.Annotations = map[string]string{annotation.InjectTrafficAgent: "enabled"}
	// The Service patch below leaves the manifest changed, so the fixture
	// must not be memoized past this test.
	wl := freshWorkload(t, ctx, r, ns, tpl)

	var before []corev1.Pod
	s.Require().Eventually(func() bool {
		pods, err := podsOf(ctx, r, ns, wl.Name)
		if err != nil || len(pods) != tpl.Replicas {
			return false
		}
		for i := range pods {
			if !podReady(&pods[i]) || !podHasAgent(&pods[i]) {
				return false
			}
		}
		before = pods
		return true
	}, agentPollTimeout, agentPollInterval, "every pod should carry a traffic-agent before the update")
	original := make(map[string]bool, len(before))
	for i := range before {
		original[before[i].Name] = true
	}
	generation := deploymentGeneration(t, ctx, r, ns, wl.Name)

	sampler := startReadySampler(ctx, r, ns, wl.Name)
	_, err := r.Kubectl(ctx, ns, "patch", "service", wl.SvcName, "--type=json",
		"-p", `[{"op":"replace","path":"/spec/ports/0/port","value":8081}]`)
	s.Require().NoError(err, "patch service %s", wl.SvcName)

	s.Require().Eventually(func() bool {
		pods, err := podsOf(ctx, r, ns, wl.Name)
		if err != nil || len(pods) != tpl.Replicas {
			return false
		}
		for i := range pods {
			if original[pods[i].Name] || !podReady(&pods[i]) || !podHasAgent(&pods[i]) {
				return false
			}
		}
		return true
	}, pacingTimeout, agentPollInterval, "every pod should be replaced by a ready one carrying the new agent config")
	minReady := sampler.stop()

	s.GreaterOrEqual(minReady, tpl.MinAvailable, "ready pods during the update never fall below minAvailable")
	s.Equal(generation, deploymentGeneration(t, ctx, r, ns, wl.Name), "no controller-driven restart was needed")
}

// podsOf returns the pods matching the app=name selector in ns.
func podsOf(ctx context.Context, r *rt.Runtime, ns, name string) ([]corev1.Pod, error) {
	out, err := r.Kubectl(ctx, ns, "get", "pod", "-l", "app="+name, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list corev1.PodList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func podReady(pod *corev1.Pod) bool {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func podHasAgent(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == agentContainerName {
			return true
		}
	}
	return false
}

// deploymentGeneration returns the Deployment's metadata.generation, which
// a restartedAt patch on the pod template would increment.
func deploymentGeneration(t testing.TB, ctx context.Context, r *rt.Runtime, ns, name string) int64 {
	t.Helper()
	out, err := r.Kubectl(ctx, ns, "get", "deployment", name, "-o", "jsonpath={.metadata.generation}")
	if err != nil {
		t.Fatalf("get deployment %s generation: %v", name, err)
	}
	gen, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		t.Fatalf("parse deployment %s generation %q: %v", name, out, err)
	}
	return gen
}

// readySampler polls the ready pod count of a workload and remembers the
// lowest value seen.
type readySampler struct {
	stopCh chan struct{}
	done   sync.WaitGroup
	min    int
}

func startReadySampler(ctx context.Context, r *rt.Runtime, ns, name string) *readySampler {
	s := &readySampler{stopCh: make(chan struct{}), min: -1}
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		for {
			if pods, err := podsOf(ctx, r, ns, name); err == nil {
				ready := 0
				for i := range pods {
					if podReady(&pods[i]) {
						ready++
					}
				}
				if s.min < 0 || ready < s.min {
					s.min = ready
				}
			}
			select {
			case <-s.stopCh:
				return
			case <-ctx.Done():
				return
			case <-time.After(250 * time.Millisecond):
			}
		}
	}()
	return s
}

// stop ends the sampling and returns the lowest ready count seen.
func (s *readySampler) stop() int {
	close(s.stopCh)
	s.done.Wait()
	return s.min
}

package trafficmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
)

func testAgentPodInfo(workload, namespace, podName string) *manager.AgentPodInfo {
	return &manager.AgentPodInfo{WorkloadName: workload, Namespace: namespace, PodName: podName, Version: "v1"}
}

// TestApplyAgentPodsDelta exercises the pure accumulation step behind
// watchSessionEvents' delta.AgentPods dispatch: upserts and removals fold
// into the running map, and the returned snapshot is the []agentPod
// projection of that map (all namespaces, not just the connected one).
func TestApplyAgentPodsDelta(t *testing.T) {
	t.Run("upserts accumulate into agentPod entries", func(t *testing.T) {
		podMap := make(map[string]*manager.AgentPodInfo)
		pods := applyAgentPodsDelta(podMap, &manager.AgentPodInfoDelta{
			Upserts: map[string]*manager.AgentPodInfo{
				"a1": testAgentPodInfo("echo", "default", "echo-1"),
				"a2": testAgentPodInfo("other", "other-ns", "other-1"),
			},
		})
		require.Len(t, pods, 2)
		byWorkload := make(map[string]agentPod, len(pods))
		for _, p := range pods {
			byWorkload[p.workload] = p
		}
		assert.Equal(t, "default", byWorkload["echo"].namespace)
		assert.Equal(t, "other-ns", byWorkload["other"].namespace)
		assert.Equal(t, "v1", byWorkload["echo"].version)
	})

	t.Run("removals drop entries from subsequent snapshots", func(t *testing.T) {
		podMap := make(map[string]*manager.AgentPodInfo)
		applyAgentPodsDelta(podMap, &manager.AgentPodInfoDelta{
			Upserts: map[string]*manager.AgentPodInfo{
				"a1": testAgentPodInfo("echo", "default", "echo-1"),
				"a2": testAgentPodInfo("other", "default", "other-1"),
			},
		})
		pods := applyAgentPodsDelta(podMap, &manager.AgentPodInfoDelta{Removals: []string{"a2"}})
		require.Len(t, pods, 1)
		assert.Equal(t, "echo", pods[0].workload)
	})

	t.Run("a pod replacement (same key, new pod name) is reflected", func(t *testing.T) {
		podMap := make(map[string]*manager.AgentPodInfo)
		applyAgentPodsDelta(podMap, &manager.AgentPodInfoDelta{
			Upserts: map[string]*manager.AgentPodInfo{"a1": testAgentPodInfo("echo", "default", "echo-1")},
		})
		pods := applyAgentPodsDelta(podMap, &manager.AgentPodInfoDelta{
			Upserts: map[string]*manager.AgentPodInfo{"a1": testAgentPodInfo("echo", "default", "echo-2")},
		})
		require.Len(t, pods, 1)
		assert.Equal(t, "echo-2", pods[0].podName)
	})
}

// TestApplyInterceptsDelta exercises the pure accumulation step behind
// watchSessionEvents' delta.Intercepts dispatch.
func TestApplyInterceptsDelta(t *testing.T) {
	icMap := make(map[string]*manager.InterceptInfo)
	ics := applyInterceptsDelta(icMap, &manager.InterceptInfoDelta{
		Upserts: map[string]*manager.InterceptInfo{
			"i1": {Id: "i1", Disposition: manager.InterceptDispositionType_ACTIVE},
		},
	})
	require.Len(t, ics, 1)

	ics = applyInterceptsDelta(icMap, &manager.InterceptInfoDelta{Removals: []string{"i1"}})
	assert.Empty(t, ics)
}

// TestCoveredCombined exercises the namespace-scoping predicate the combined
// loop passes to handleAgentPodSnapshot / cancelUnwanted: the connected
// namespace and the explicitly requested (port-forwardable) namespaces are
// covered, anything else is not.
func TestCoveredCombined(t *testing.T) {
	s := &session{
		Cluster: &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Namespace: "default"}},
	}
	s.agentPodWatchNamespacesValue = []string{"team-a", "team-b"}
	s.agentPodWatchNamespacesOnce.Do(func() {}) // pre-seed so agentPodWatchNamespaces() doesn't recompute

	assert.True(t, s.coveredCombined("default"))
	assert.True(t, s.coveredCombined("team-a"))
	assert.True(t, s.coveredCombined("team-b"))
	assert.False(t, s.coveredCombined("team-c"))
}

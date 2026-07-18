package trafficmgr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// TestDecideIngestPod exercises the pure pod-matching decision behind
// handleAgentPodSnapshot's ingest walk: workload+namespace matching (not
// workload-only, the pre-existing cross-namespace hijack bug this rework
// fixes) and replacement detection by pod name.
func TestDecideIngestPod(t *testing.T) {
	key := ingestKey{workload: "echo", namespace: "default", container: "cn"}

	t.Run("no pods for workload+namespace", func(t *testing.T) {
		pods := []agentPod{
			{workload: "other", namespace: "default", podName: "other-1"},
			{workload: "echo", namespace: "other-ns", podName: "echo-1"},
		}
		decision, matching := decideIngestPod(pods, key, "echo-1")
		assert.Equal(t, ingestPodNoMatch, decision)
		assert.Nil(t, matching)
	})

	t.Run("current pod still present keeps alive", func(t *testing.T) {
		pods := []agentPod{
			{workload: "echo", namespace: "default", podName: "echo-1"},
			{workload: "echo", namespace: "default", podName: "echo-2"},
		}
		decision, matching := decideIngestPod(pods, key, "echo-1")
		assert.Equal(t, ingestPodKeepAlive, decision)
		assert.Len(t, matching, 2)
	})

	t.Run("current pod gone but sibling pods remain means replaced", func(t *testing.T) {
		pods := []agentPod{
			{workload: "echo", namespace: "default", podName: "echo-2"},
		}
		decision, matching := decideIngestPod(pods, key, "echo-1")
		assert.Equal(t, ingestPodReplaced, decision)
		require.Len(t, matching, 1)
		assert.Equal(t, "echo-2", matching[0].podName)
	})

	t.Run("same workload name in a different namespace does not match", func(t *testing.T) {
		// This is the namespace-blind matching bug the rework fixes: a
		// same-named workload in another namespace must not be treated as a
		// match for this ingest's key.
		pods := []agentPod{
			{workload: "echo", namespace: "other-ns", podName: "echo-1"},
		}
		decision, matching := decideIngestPod(pods, key, "echo-1")
		assert.Equal(t, ingestPodNoMatch, decision)
		assert.Nil(t, matching)
	})
}

// TestSelectReplacementAgent exercises the pure candidate-selection step used
// after an EnsureAgent refetch: prefer a candidate whose pod is among the
// snapshot's matching pods, falling back to the first candidate otherwise.
func TestSelectReplacementAgent(t *testing.T) {
	matching := []agentPod{{workload: "echo", namespace: "default", podName: "echo-2"}}

	t.Run("picks the candidate matching the snapshot", func(t *testing.T) {
		candidates := []*manager.AgentInfo{
			{PodName: "echo-1"},
			{PodName: "echo-2"},
		}
		ai := selectReplacementAgent(candidates, matching)
		require.NotNil(t, ai)
		assert.Equal(t, "echo-2", ai.PodName)
	})

	t.Run("falls back to the first candidate when none matches", func(t *testing.T) {
		candidates := []*manager.AgentInfo{{PodName: "echo-9"}}
		ai := selectReplacementAgent(candidates, matching)
		require.NotNil(t, ai)
		assert.Equal(t, "echo-9", ai.PodName)
	})

	t.Run("nil when there are no candidates", func(t *testing.T) {
		assert.Nil(t, selectReplacementAgent(nil, matching))
	})
}

func TestAgentPodFromPodInfo(t *testing.T) {
	ap := &manager.AgentPodInfo{
		WorkloadName: "echo",
		Namespace:    "default",
		PodName:      "echo-1",
		Version:      "2.30.0",
		NodeAgent:    true,
	}
	got := agentPodFromPodInfo(ap)
	assert.Equal(t, agentPod{workload: "echo", namespace: "default", podName: "echo-1", version: "2.30.0", nodeAgent: true}, got)
}

func TestAgentPodFromAgentInfo(t *testing.T) {
	ai := &manager.AgentInfo{
		Name:      "echo",
		Namespace: "default",
		PodName:   "echo-1",
		Version:   "2.30.0",
		NodeAgent: true,
	}
	got := agentPodFromAgentInfo(ai)
	assert.Equal(t, agentPod{workload: "echo", namespace: "default", podName: "echo-1", version: "2.30.0", nodeAgent: true}, got)
}

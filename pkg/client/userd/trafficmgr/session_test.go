package trafficmgr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
)

// TestAgentPodWatchNamespaces asserts that the watch set is every currently
// mapped namespace, with no permission-based filtering: whether a namespace
// yields a direct connection is decided later, when its agent is dialed.
func TestAgentPodWatchNamespaces(t *testing.T) {
	t.Run("disabled returns nil", func(t *testing.T) {
		cfg := client.GetDefaultConfig()
		cfg.Cluster().AgentPortForward = false
		ctx := client.WithConfig(context.Background(), cfg)
		s := &session{Cluster: &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: ctx, Namespace: "default"}}}
		require.Empty(t, s.agentPodWatchNamespaces())
	})

	t.Run("enabled returns all mapped namespaces unfiltered", func(t *testing.T) {
		cfg := client.GetDefaultConfig()
		cfg.Cluster().AgentPortForward = true
		// An external manager address makes the cluster treat mapped
		// namespaces as accessible without any SelfSubjectAccessReview,
		// so the test needs no live Kubernetes API access.
		cfg.Cluster().ManagerAddress = "manager.example:443"
		ctx := client.WithConfig(context.Background(), cfg)
		cluster := &k8s.Cluster{Kubeconfig: &k8s.Kubeconfig{Context: ctx, Namespace: "default"}}
		cluster.SetMappedNamespaces([]string{"team-b", "team-a"})

		s := &session{Cluster: cluster}
		require.Equal(t, []string{"team-a", "team-b"}, s.agentPodWatchNamespaces())
	})
}

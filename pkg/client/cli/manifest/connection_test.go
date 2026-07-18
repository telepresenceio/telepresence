package manifest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSubnetViaWorkloads(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		svs, err := buildSubnetViaWorkloads(nil)
		require.NoError(t, err)
		assert.Nil(t, svs)
	})

	t.Run("CIDR and symbolic pass through", func(t *testing.T) {
		svs, err := buildSubnetViaWorkloads([]ProxyVia{
			{Subnet: "10.100.0.0/16", Workload: "local"},
			{Subnet: "pods", Workload: "echo-server"},
		})
		require.NoError(t, err)
		require.Len(t, svs, 2)
		assert.Equal(t, "10.100.0.0/16", svs[0].Subnet)
		assert.Equal(t, "local", svs[0].Workload)
		assert.Equal(t, "pods", svs[1].Subnet)
		assert.Equal(t, "echo-server", svs[1].Workload)
	})

	t.Run("all expands to also, pods, service", func(t *testing.T) {
		svs, err := buildSubnetViaWorkloads([]ProxyVia{{Subnet: "all", Workload: "local"}})
		require.NoError(t, err)
		require.Len(t, svs, 3)
		subnets := []string{svs[0].Subnet, svs[1].Subnet, svs[2].Subnet}
		assert.ElementsMatch(t, []string{"also", "pods", "service"}, subnets)
	})

	t.Run("overlapping CIDRs are rejected", func(t *testing.T) {
		_, err := buildSubnetViaWorkloads([]ProxyVia{
			{Subnet: "10.0.0.0/8", Workload: "a"},
			{Subnet: "10.1.0.0/16", Workload: "b"},
		})
		require.Error(t, err)
	})

	t.Run("invalid CIDR is rejected", func(t *testing.T) {
		_, err := buildSubnetViaWorkloads([]ProxyVia{{Subnet: "not-a-cidr", Workload: "a"}})
		require.Error(t, err)
	})
}

func TestBuildConnectRequest(t *testing.T) {
	conn := &Connection{
		Name:      "dev",
		Context:   "kind-dev",
		Namespace: "default",
		KubeFlags: map[string]string{"request-timeout": "30s"},
	}
	cr, err := buildConnectRequest(conn)
	require.NoError(t, err)
	assert.Equal(t, "dev", cr.Name)
	assert.Equal(t, "kind-dev", cr.KubeFlags["context"])
	assert.Equal(t, "default", cr.KubeFlags["namespace"])
	assert.Equal(t, "30s", cr.KubeFlags["request-timeout"])
}

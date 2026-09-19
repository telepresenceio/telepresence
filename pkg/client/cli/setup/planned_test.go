package setup

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

func TestPlannedObjects(t *testing.T) {
	values := &helm.Values{
		AgentInjector: helm.AgentInjector{Enabled: new(false)},
		NodeAgent:     helm.NodeAgent{Enabled: new(true)},
		QuicTunnel:    helm.QuicTunnel{Enabled: new(true)},
	}
	objects, err := PlannedObjects(context.Background(), "ambassador", values)
	require.NoError(t, err)
	require.NotEmpty(t, objects)

	assert.True(t, sort.StringsAreSorted(objects), "objects must be sorted (and thereby kind-grouped)")
	assert.Contains(t, objects, "StatefulSet traffic-manager.ambassador")
	assert.Contains(t, objects, "Deployment quic-forwarder.ambassador")
	assert.Contains(t, objects, "ServiceAccount traffic-manager.ambassador")
	assert.Contains(t, objects, "ClusterRole traffic-manager-ambassador")
	assert.NotContains(t, objects, "MutatingWebhookConfiguration agent-injector-webhook-ambassador",
		"a disabled agent-injector must not plan its webhook")
}

func TestPlannedObjects_InjectorEnabled(t *testing.T) {
	values := &helm.Values{
		AgentInjector: helm.AgentInjector{Enabled: new(true)},
		NodeAgent:     helm.NodeAgent{Enabled: new(false)},
		QuicTunnel:    helm.QuicTunnel{Enabled: new(false)},
	}
	objects, err := PlannedObjects(context.Background(), "ambassador", values)
	require.NoError(t, err)
	assert.Contains(t, objects, "MutatingWebhookConfiguration agent-injector-webhook-ambassador")
	assert.NotContains(t, objects, "Deployment quic-forwarder.ambassador")
}

package setup

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlannedObjects(t *testing.T) {
	values := map[string]any{
		"agentInjector": map[string]any{"enabled": false},
		"nodeAgent":     map[string]any{"enabled": true},
		"quicTunnel":    map[string]any{"enabled": true},
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
	values := map[string]any{
		"agentInjector": map[string]any{"enabled": true},
		"nodeAgent":     map[string]any{"enabled": false},
		"quicTunnel":    map[string]any{"enabled": false},
	}
	objects, err := PlannedObjects(context.Background(), "ambassador", values)
	require.NoError(t, err)
	assert.Contains(t, objects, "MutatingWebhookConfiguration agent-injector-webhook-ambassador")
	assert.NotContains(t, objects, "Deployment quic-forwarder.ambassador")
}

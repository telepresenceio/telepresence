package trafficmgr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEffectiveMappedNamespacesRejectsUnmanagedRequestedNamespace(t *testing.T) {
	namespaces, err := effectiveMappedNamespaces(
		[]string{"alpha", "beta"},
		nil,
		[]string{"alpha"},
	)

	require.Error(t, err)
	require.Nil(t, namespaces)
	require.Contains(t, err.Error(), `mapped namespaces ["beta"] are not managed by this traffic-manager`)
	require.Contains(t, err.Error(), `managed namespaces are ["alpha"]`)
}

func TestEffectiveMappedNamespacesRejectsUnmanagedClientConfigNamespace(t *testing.T) {
	namespaces, err := effectiveMappedNamespaces(
		nil,
		[]string{"beta"},
		[]string{"alpha"},
	)

	require.Error(t, err)
	require.Nil(t, namespaces)
	require.Contains(t, err.Error(), `mapped namespaces ["beta"] are not managed by this traffic-manager`)
}

func TestEffectiveMappedNamespacesUsesManagerNamespacesWhenAllRequested(t *testing.T) {
	namespaces, err := effectiveMappedNamespaces(
		[]string{"all"},
		[]string{"beta"},
		[]string{"alpha", "gamma"},
	)

	require.NoError(t, err)
	require.Equal(t, []string{"alpha", "gamma"}, namespaces)
}

func TestEffectiveMappedNamespacesAllowsGlobalManager(t *testing.T) {
	namespaces, err := effectiveMappedNamespaces(
		[]string{"beta"},
		nil,
		nil,
	)

	require.NoError(t, err)
	require.Equal(t, []string{"beta"}, namespaces)
}

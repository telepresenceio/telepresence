package trafficmgr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEffectiveMappedNamespacesAllowsUnmanagedRequestedNamespace(t *testing.T) {
	// A requested namespace the traffic-manager does not manage is still mapped
	// for DNS; mapping does not require management.
	namespaces := effectiveMappedNamespaces(
		[]string{"alpha", "beta"},
		nil,
		[]string{"alpha"},
	)
	require.Equal(t, []string{"alpha", "beta"}, namespaces)
}

func TestEffectiveMappedNamespacesAllowsUnmanagedClientConfigNamespace(t *testing.T) {
	namespaces := effectiveMappedNamespaces(
		nil,
		[]string{"beta"},
		[]string{"alpha"},
	)
	require.Equal(t, []string{"beta"}, namespaces)
}

func TestEffectiveMappedNamespacesUsesManagerNamespacesWhenAllRequested(t *testing.T) {
	namespaces := effectiveMappedNamespaces(
		[]string{"all"},
		[]string{"beta"},
		[]string{"alpha", "gamma"},
	)
	require.Equal(t, []string{"alpha", "gamma"}, namespaces)
}

func TestEffectiveMappedNamespacesAllowsGlobalManager(t *testing.T) {
	namespaces := effectiveMappedNamespaces(
		[]string{"beta"},
		nil,
		nil,
	)
	require.Equal(t, []string{"beta"}, namespaces)
}

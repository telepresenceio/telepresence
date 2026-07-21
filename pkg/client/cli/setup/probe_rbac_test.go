package setup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestProbeRBAC_AllowAll(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*authv1.ResourceAttributes) bool { return true })

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	assert.Equal(t, VerdictYes, facts.ClusterWide.Verdict)
	assert.Empty(t, facts.Missing)
	assert.Empty(t, facts.MissingAttributes)
	assert.Equal(t, VerdictYes, facts.Namespaced.Verdict)
}

// TestProbeRBAC_ClusterScopedDenied denies exactly the two objects that only
// the cluster-wide render creates (the "traffic-manager-ambassador"
// ClusterRole/ClusterRoleBinding pair; the namespace-scoped render creates a
// differently named pair, "traffic-manager-cluster-wide-ambassador", for the
// servicecidr watch). This proves the namespaced fallback
// actually re-renders and re-checks rather than reusing the cluster-wide
// verdict.
func TestProbeRBAC_ClusterScopedDenied(t *testing.T) {
	const deniedName = "traffic-manager-ambassador"
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *authv1.ResourceAttributes) bool {
		if ra.Name == deniedName && (ra.Resource == "clusterroles" || ra.Resource == "clusterrolebindings") {
			return false
		}
		return true
	})

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	require.Equal(t, VerdictNo, facts.ClusterWide.Verdict)
	assert.ElementsMatch(t, []string{
		"create clusterroles.rbac.authorization.k8s.io",
		"create clusterrolebindings.rbac.authorization.k8s.io",
	}, facts.Missing)

	// The structured denials are the raw material RBAC generation uses; they
	// must stay in sync with the formatted strings, one-to-one, in the same
	// (sorted) order.
	require.Len(t, facts.MissingAttributes, len(facts.Missing))
	for i, a := range facts.MissingAttributes {
		assert.Equal(t, facts.Missing[i], formatAttributes(&authv1.ResourceAttributes{
			Verb: a.Verb, Group: a.Group, Resource: a.Resource, Namespace: a.Namespace, Name: a.Name,
		}))
	}
	assert.ElementsMatch(t, []DeniedAttribute{
		{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: deniedName},
		{Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: deniedName},
	}, facts.MissingAttributes)

	require.Equal(t, VerdictYes, facts.Namespaced.Verdict)
	assert.Empty(t, facts.MissingNamespaced)
	assert.Empty(t, facts.MissingNamespacedAttributes)
}

// TestProbeRBAC_NamespacedAttributes proves the namespaced fallback records
// its own structured denials, separate from the cluster-wide ones.
func TestProbeRBAC_NamespacedAttributes(t *testing.T) {
	client := fake.NewClientset()
	// Deny everything, so both the cluster-wide and namespaced renders come
	// back denied, and both record structured denials.
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*authv1.ResourceAttributes) bool { return false })

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	require.Equal(t, VerdictNo, facts.ClusterWide.Verdict)
	require.NotEmpty(t, facts.MissingAttributes)
	require.Equal(t, VerdictNo, facts.Namespaced.Verdict)
	require.NotEmpty(t, facts.MissingNamespacedAttributes)

	for _, a := range facts.MissingNamespacedAttributes {
		assert.NotEmpty(t, a.Verb)
		assert.NotEmpty(t, a.Resource)
	}
}

func TestProbeRBAC_ReviewCallError(t *testing.T) {
	client := fake.NewClientset()
	// No InstallFakeSelfSubjectAccessReviews: the fake clientset has no reactor
	// registered for SelfSubjectAccessReview, so every review call errors.
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	assert.Equal(t, VerdictUnknown, facts.ClusterWide.Verdict)
	assert.NotEmpty(t, facts.ClusterWide.Evidence)
}

func TestFormatAttributes(t *testing.T) {
	assert.Equal(t, "create deployments.apps in namespace ambassador", formatAttributes(&authv1.ResourceAttributes{
		Verb: "create", Group: "apps", Resource: "deployments", Namespace: "ambassador",
	}))
	assert.Equal(t, "create clusterroles.rbac.authorization.k8s.io", formatAttributes(&authv1.ResourceAttributes{
		Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles",
	}))
	assert.Equal(t, "create namespaces", formatAttributes(&authv1.ResourceAttributes{
		Verb: "create", Resource: "namespaces",
	}))
}

func TestDedupeAttributes(t *testing.T) {
	ras := []*authv1.ResourceAttributes{
		{Verb: "create", Resource: "secrets", Namespace: "ambassador"},
		{Verb: "create", Resource: "secrets", Namespace: "ambassador"},
		{Verb: "create", Resource: "secrets", Namespace: "other"},
	}
	assert.Len(t, dedupeAttributes(ras), 2)
}

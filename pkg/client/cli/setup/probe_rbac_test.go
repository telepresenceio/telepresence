package setup

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auth "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/rbacgen"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestProbeRBAC_AllowAll(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*auth.ResourceAttributes) bool { return true })

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	assert.Equal(t, VerdictYes, facts.ClusterWide.Verdict)
	assert.Empty(t, facts.Missing)
	assert.Empty(t, facts.MissingAttributes)
	assert.Equal(t, VerdictYes, facts.Namespaced.Verdict)
}

// TestProbeRBAC_ClusterRolesDenied denies creating ClusterRoles. Both renders
// create one, so both are denied, and each reports the ClusterRole of its own
// render, which proves that the namespaced fallback re-renders rather than
// reusing the cluster-wide result.
func TestProbeRBAC_ClusterRolesDenied(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *auth.ResourceAttributes) bool {
		return !(ra.Verb == "create" && ra.Resource == "clusterroles")
	})

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	require.Equal(t, VerdictNo, facts.ClusterWide.Verdict)
	require.NotEmpty(t, facts.Missing)
	for _, m := range facts.Missing {
		assert.Equal(t, "create clusterroles.rbac.authorization.k8s.io", m)
	}

	// The structured denials must stay in sync with the formatted strings,
	// one-to-one, in the same (sorted) order.
	require.Len(t, facts.MissingAttributes, len(facts.Missing))
	for i, a := range facts.MissingAttributes {
		assert.Equal(t, facts.Missing[i], rbacgen.FormatAttributes(&auth.ResourceAttributes{
			Verb: a.Verb, Group: a.Group, Resource: a.Resource, Subresource: a.Subresource, Namespace: a.Namespace, Name: a.Name,
		}))
	}
	assert.Contains(t, facts.MissingAttributes, rbacgen.DeniedAttribute{
		Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "traffic-manager-ambassador",
	})

	require.Equal(t, VerdictNo, facts.Namespaced.Verdict)
	assert.Contains(t, facts.MissingNamespacedAttributes, rbacgen.DeniedAttribute{
		Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "traffic-manager-cluster-wide-ambassador",
	})
	assert.NotContains(t, facts.MissingNamespacedAttributes, rbacgen.DeniedAttribute{
		Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "traffic-manager-ambassador",
	})
}

// TestProbeRBAC_NamedCreateGrant models a ClusterRole that grants create on
// clusterroles with resourceNames. The API server never lets such a rule
// allow a create, so the probe must not count it, and no create review may
// carry a name.
func TestProbeRBAC_NamedCreateGrant(t *testing.T) {
	var mu sync.Mutex
	var namedCreates []string
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *auth.ResourceAttributes) bool {
		if ra.Verb == "create" && ra.Name != "" {
			mu.Lock()
			namedCreates = append(namedCreates, ra.Resource+"/"+ra.Name)
			mu.Unlock()
		}
		if ra.Verb == "create" && ra.Resource == "clusterroles" {
			return ra.Name == "traffic-manager-ambassador"
		}
		return true
	})

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	assert.Empty(t, namedCreates)
	require.Equal(t, VerdictNo, facts.ClusterWide.Verdict)
	assert.Contains(t, facts.MissingAttributes, rbacgen.DeniedAttribute{
		Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "traffic-manager-ambassador",
	})
}

// TestProbeRBAC_NamespacedAttributes proves the namespaced fallback records
// its own structured denials, separate from the cluster-wide ones.
func TestProbeRBAC_NamespacedAttributes(t *testing.T) {
	client := fake.NewClientset()
	// Deny everything, so both the cluster-wide and namespaced renders come
	// back denied, and both record structured denials.
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*auth.ResourceAttributes) bool { return false })

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

func TestProbeRBAC_X509KubeSystemAllowed(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*auth.ResourceAttributes) bool { return true })

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	assert.Equal(t, VerdictYes, facts.X509KubeSystem.Verdict)
}

func TestProbeRBAC_X509KubeSystemDenied(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *auth.ResourceAttributes) bool {
		if ra.Resource == "rolebindings" && ra.Namespace == "kube-system" {
			return false
		}
		return true
	})

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	require.Equal(t, VerdictNo, facts.X509KubeSystem.Verdict)
	assert.NotEmpty(t, facts.X509KubeSystem.Evidence)
}

func TestProbeRBAC_CertManagerCertificateAllowed(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*auth.ResourceAttributes) bool { return true })

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	assert.Equal(t, VerdictYes, facts.CertManagerCertificate.Verdict)
}

func TestProbeRBAC_CertManagerCertificateDenied(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *auth.ResourceAttributes) bool {
		if ra.Resource == certManagerResource && ra.Group == "cert-manager.io" {
			return false
		}
		return true
	})

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	require.Equal(t, VerdictNo, facts.CertManagerCertificate.Verdict)
	assert.NotEmpty(t, facts.CertManagerCertificate.Evidence)
}

// TestDefaultCandidateValues_ExternalEndpointSweep proves the P1 render also
// enables the external endpoint (its Service enters the privilege sweep)
// without pulling in x509 auth's kube-system RoleBinding.
func TestDefaultCandidateValues_ExternalEndpointSweep(t *testing.T) {
	render, err := rbacgen.RenderChart(context.Background(), "ambassador", DefaultCandidateValues())
	require.NoError(t, err)

	var foundExternalService bool
	for _, o := range render.All() {
		gvk := o.GroupVersionKind()
		if gvk.Kind == "Service" && o.GetName() == "traffic-manager-external" {
			foundExternalService = true
		}
		if gvk.Kind == "RoleBinding" && o.GetNamespace() == "kube-system" {
			t.Fatalf("unexpected kube-system RoleBinding in the P1 render: %s", o.GetName())
		}
	}
	assert.True(t, foundExternalService, "expected a traffic-manager-external Service in the P1 render")
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

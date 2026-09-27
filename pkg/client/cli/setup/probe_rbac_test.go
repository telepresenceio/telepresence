package setup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	auth "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// TestProbeRBAC_KafkaOnlyDenial denies exactly the two privileges enabling
// the Kafka provider adds on top of the base candidate render: creating its
// ValidatingWebhookConfiguration, and creating its two CRDs. Neither object
// exists in the base render, so ClusterWide stays fully permitted while
// Kafka is denied.
func TestProbeRBAC_KafkaOnlyDenial(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *auth.ResourceAttributes) bool {
		if ra.Verb == "create" && (ra.Resource == "validatingwebhookconfigurations" || ra.Resource == "customresourcedefinitions") {
			return false
		}
		return true
	})

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	require.Equal(t, VerdictYes, facts.ClusterWide.Verdict)
	assert.Empty(t, facts.Missing)

	require.Equal(t, VerdictNo, facts.Kafka.Verdict)
	assert.ElementsMatch(t, []string{
		"create validatingwebhookconfigurations.admissionregistration.k8s.io",
		"create customresourcedefinitions.apiextensions.k8s.io",
		"create customresourcedefinitions.apiextensions.k8s.io",
	}, facts.MissingKafka)
	require.Len(t, facts.MissingKafkaAttributes, len(facts.MissingKafka))
}

func TestProbeRBAC_KafkaAllowed(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*auth.ResourceAttributes) bool { return true })

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeRBAC(context.Background(), true)

	assert.Equal(t, VerdictYes, facts.Kafka.Verdict)
	assert.Empty(t, facts.MissingKafka)
	assert.Empty(t, facts.MissingKafkaAttributes)
}

func TestSubtractAttributes(t *testing.T) {
	base := []*auth.ResourceAttributes{
		{Verb: "create", Resource: "secrets", Namespace: "ambassador"},
		{Verb: "create", Resource: "deployments", Group: "apps", Namespace: "ambassador", Name: "traffic-manager"},
	}
	all := []*auth.ResourceAttributes{
		{Verb: "create", Resource: "secrets", Namespace: "ambassador"},
		{Verb: "create", Resource: "deployments", Group: "apps", Namespace: "ambassador", Name: "traffic-manager"},
		{Verb: "create", Resource: "deployments", Group: "apps", Namespace: "ambassador", Name: "tp-kafka"},
		{Verb: "create", Resource: "validatingwebhookconfigurations", Group: "admissionregistration.k8s.io", Name: "tp-kafka-ambassador"},
	}
	diff := subtractAttributes(all, base)
	require.Len(t, diff, 2)
	assert.ElementsMatch(t, []string{"deployments", "validatingwebhookconfigurations"}, []string{diff[0].Resource, diff[1].Resource})
}

// TestChartAttributes_KafkaCandidate proves the Kafka-enabled render adds the
// tp-kafka Deployment that the base render never includes.
func TestChartAttributes_KafkaCandidate(t *testing.T) {
	p := &Prober{ManagerNamespace: "ambassador"}
	base := DefaultCandidateValues()

	baseAttrs, err := p.chartAttributes(context.Background(), true, base)
	require.NoError(t, err)
	assert.False(t, hasResourceNamed(baseAttrs, "deployments", "tp-kafka"), "base render should not include the tp-kafka Deployment")

	kafkaValues := base.DeepCopy()
	kafkaValues.Kafka.Enabled = new(true)
	kafkaAttrs, err := p.chartAttributes(context.Background(), true, kafkaValues)
	require.NoError(t, err)
	assert.True(t, hasResourceNamed(kafkaAttrs, "deployments", "tp-kafka"), "Kafka-enabled render should include the tp-kafka Deployment")
}

func hasResourceNamed(ras []*auth.ResourceAttributes, resource, name string) bool {
	for _, ra := range ras {
		if ra.Resource == resource && ra.Name == name {
			return true
		}
	}
	return false
}

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
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *auth.ResourceAttributes) bool {
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
		assert.Equal(t, facts.Missing[i], formatAttributes(&auth.ResourceAttributes{
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
	const deniedName = "traffic-manager-x509-auth-ambassador"
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *auth.ResourceAttributes) bool {
		if ra.Resource == "rolebindings" && ra.Namespace == "kube-system" && ra.Name == deniedName {
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
	chrt, err := loadEmbeddedChart()
	require.NoError(t, err)
	manifest, err := renderChart(context.Background(), chrt, "ambassador", DefaultCandidateValues())
	require.NoError(t, err)
	objs, err := decodeManifests(manifest)
	require.NoError(t, err)

	var foundExternalService bool
	for _, o := range objs {
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

func TestFormatAttributes(t *testing.T) {
	assert.Equal(t, "create deployments.apps in namespace ambassador", formatAttributes(&auth.ResourceAttributes{
		Verb: "create", Group: "apps", Resource: "deployments", Namespace: "ambassador",
	}))
	assert.Equal(t, "create clusterroles.rbac.authorization.k8s.io", formatAttributes(&auth.ResourceAttributes{
		Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles",
	}))
	assert.Equal(t, "create namespaces", formatAttributes(&auth.ResourceAttributes{
		Verb: "create", Resource: "namespaces",
	}))
}

func TestDedupeAttributes(t *testing.T) {
	ras := []*auth.ResourceAttributes{
		{Verb: "create", Resource: "secrets", Namespace: "ambassador"},
		{Verb: "create", Resource: "secrets", Namespace: "ambassador"},
		{Verb: "create", Resource: "secrets", Namespace: "other"},
	}
	assert.Len(t, dedupeAttributes(ras), 2)
}

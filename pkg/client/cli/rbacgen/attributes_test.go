package rbacgen

import (
	"testing"

	"github.com/stretchr/testify/assert"

	auth "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

func object(apiVersion, kind, name, namespace string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion(apiVersion)
	o.SetKind(kind)
	o.SetName(name)
	o.SetNamespace(namespace)
	return o
}

func TestCreateAttributes(t *testing.T) {
	ras := CreateAttributes([]*unstructured.Unstructured{
		object("apps/v1", "Deployment", "d", ""),
		object("rbac.authorization.k8s.io/v1", "RoleBinding", "rb", "kube-system"),
		object("rbac.authorization.k8s.io/v1", "ClusterRole", "cr", "ignored"),
		object("cert-manager.io/v1", "ClusterIssuer", "ci", "ignored"),
	}, "ambassador")
	assert.Equal(t, []*auth.ResourceAttributes{
		{Verb: "create", Group: "apps", Version: "v1", Resource: "deployments", Namespace: "ambassador", Name: "d"},
		{Verb: "create", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings", Namespace: "kube-system", Name: "rb"},
		{Verb: "create", Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles", Name: "cr"},
		{Verb: "create", Group: "cert-manager.io", Version: "v1", Resource: "clusterissuers", Name: "ci"},
	}, ras)
}

func TestInstallAttributes_Extras(t *testing.T) {
	ras := InstallAttributes(nil, "ambassador", false, &helm.Values{NodeAgent: helm.NodeAgent{Enabled: new(true)}})
	assert.ElementsMatch(t, []*auth.ResourceAttributes{
		{Verb: "create", Resource: "namespaces"},
		{Verb: "create", Resource: "secrets", Namespace: "ambassador"},
		{Verb: "patch", Resource: "namespaces", Name: "ambassador"},
	}, ras)

	ras = InstallAttributes(nil, "ambassador", true, &helm.Values{})
	assert.Equal(t, []*auth.ResourceAttributes{{Verb: "create", Resource: "secrets", Namespace: "ambassador"}}, ras)
}

func TestFormatAttributes(t *testing.T) {
	assert.Equal(t, "create deployments.apps in namespace ambassador", FormatAttributes(&auth.ResourceAttributes{
		Verb: "create", Group: "apps", Resource: "deployments", Namespace: "ambassador",
	}))
	assert.Equal(t, "create clusterroles.rbac.authorization.k8s.io", FormatAttributes(&auth.ResourceAttributes{
		Verb: "create", Group: "rbac.authorization.k8s.io", Resource: "clusterroles",
	}))
	assert.Equal(t, "create namespaces", FormatAttributes(&auth.ResourceAttributes{
		Verb: "create", Resource: "namespaces",
	}))
	assert.Equal(t, "create pods/portforward in namespace ambassador", FormatAttributes(&auth.ResourceAttributes{
		Verb: "create", Resource: "pods", Subresource: "portforward", Namespace: "ambassador",
	}))
}

func TestDedupeAttributes(t *testing.T) {
	ras := []*auth.ResourceAttributes{
		{Verb: "create", Resource: "secrets", Namespace: "ambassador"},
		{Verb: "create", Resource: "secrets", Namespace: "ambassador"},
		{Verb: "create", Resource: "secrets", Namespace: "other"},
	}
	assert.Len(t, DedupeAttributes(ras), 2)
}

func TestToDeniedAttribute(t *testing.T) {
	assert.Equal(t,
		DeniedAttribute{Verb: "create", Resource: "pods", Subresource: "eviction", Namespace: "ns", Name: "p"},
		ToDeniedAttribute(&auth.ResourceAttributes{Verb: "create", Version: "v1", Resource: "pods", Subresource: "eviction", Namespace: "ns", Name: "p"}))
}

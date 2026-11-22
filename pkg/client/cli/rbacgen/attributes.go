package rbacgen

import (
	"strings"

	auth "k8s.io/api/authorization/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

// clusterScopedKinds are the chart's kinds that never carry a namespace.
var clusterScopedKinds = map[string]bool{ //nolint:gochecknoglobals // constant lookup table
	"Namespace":                      true,
	"ClusterRole":                    true,
	"ClusterRoleBinding":             true,
	"MutatingWebhookConfiguration":   true,
	"ValidatingWebhookConfiguration": true,
	"CustomResourceDefinition":       true,
	"PriorityClass":                  true,
}

// IsClusterScoped reports whether objects of kind never carry a namespace.
func IsClusterScoped(kind string) bool {
	return clusterScopedKinds[kind]
}

// ObjectNamespace returns the namespace obj is created in: empty for a
// cluster-scoped kind, and managerNamespace for a namespaced object that
// doesn't name one.
func ObjectNamespace(obj *unstructured.Unstructured, managerNamespace string) string {
	if IsClusterScoped(obj.GetKind()) {
		return ""
	}
	if ns := obj.GetNamespace(); ns != "" {
		return ns
	}
	return managerNamespace
}

// DeniedAttribute is a JSON-clean mirror of the auth.ResourceAttributes an
// access review denied, used in --format json/yaml output so an admin can
// see exactly which privileges to grant.
type DeniedAttribute struct {
	Verb        string `json:"verb"`
	Group       string `json:"group,omitempty"`
	Resource    string `json:"resource"`
	Subresource string `json:"subresource,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Name        string `json:"name,omitempty"`
}

// ToDeniedAttribute mirrors ra into its JSON-clean form.
func ToDeniedAttribute(ra *auth.ResourceAttributes) DeniedAttribute {
	return DeniedAttribute{
		Verb:        ra.Verb,
		Group:       ra.Group,
		Resource:    ra.Resource,
		Subresource: ra.Subresource,
		Namespace:   ra.Namespace,
		Name:        ra.Name,
	}
}

// CreateAttributes builds a "create" ResourceAttributes for every object.
// Name is included even though plain RBAC ignores resourceNames on create,
// because it lets a name-scoped admission policy distinguish same-typed
// objects.
func CreateAttributes(objs []*unstructured.Unstructured, managerNamespace string) []*auth.ResourceAttributes {
	ras := make([]*auth.ResourceAttributes, 0, len(objs))
	for _, obj := range objs {
		gvr, _ := apimeta.UnsafeGuessKindToResource(obj.GroupVersionKind())
		ras = append(ras, &auth.ResourceAttributes{
			Verb:      "create",
			Group:     gvr.Group,
			Version:   gvr.Version,
			Resource:  gvr.Resource,
			Namespace: ObjectNamespace(obj, managerNamespace),
			Name:      obj.GetName(),
		})
	}
	return ras
}

// InstallAttributes returns the deduplicated attributes for creating objs,
// plus the install-time needs that no rendered object implies: namespace
// creation, the Helm storage driver's secrets, and the privileged PSS label
// patch that node-agent mode requires.
func InstallAttributes(objs []*unstructured.Unstructured, managerNamespace string, nsExists bool, values *helm.Values) []*auth.ResourceAttributes {
	ras := CreateAttributes(objs, managerNamespace)
	if !nsExists {
		ras = append(ras, &auth.ResourceAttributes{Verb: "create", Resource: "namespaces"})
	}
	ras = append(ras, &auth.ResourceAttributes{Verb: "create", Resource: "secrets", Namespace: managerNamespace})
	if e := values.NodeAgent.Enabled; e != nil && *e {
		ras = append(ras, &auth.ResourceAttributes{Verb: "patch", Resource: "namespaces", Name: managerNamespace})
	}
	return DedupeAttributes(ras)
}

// AttributeKey renders every field an access review compares into a single
// string, used for both deduplication and deterministic ordering.
func AttributeKey(ra *auth.ResourceAttributes) string {
	return strings.Join([]string{ra.Verb, ra.Group, ra.Version, ra.Resource, ra.Subresource, ra.Namespace, ra.Name}, "|")
}

// DedupeAttributes removes ResourceAttributes that are identical in every
// field an access review compares.
func DedupeAttributes(ras []*auth.ResourceAttributes) []*auth.ResourceAttributes {
	seen := make(map[string]bool, len(ras))
	out := make([]*auth.ResourceAttributes, 0, len(ras))
	for _, ra := range ras {
		key := AttributeKey(ra)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ra)
	}
	return out
}

// FormatAttributes renders an attribute set like "create deployments.apps in
// namespace ambassador" or "create pods/portforward in namespace ambassador".
func FormatAttributes(ra *auth.ResourceAttributes) string {
	res := ra.Resource
	if ra.Subresource != "" {
		res += "/" + ra.Subresource
	}
	if ra.Group != "" {
		res += "." + ra.Group
	}
	s := ra.Verb + " " + res
	if ra.Namespace != "" {
		s += " in namespace " + ra.Namespace
	}
	return s
}

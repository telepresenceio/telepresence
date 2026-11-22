package rbacgen

import (
	"context"
	"fmt"
	"slices"
	"strings"

	auth "k8s.io/api/authorization/v1"
	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

// Verbs selects the Helm operations an install set covers.
type Verbs string

const (
	// VerbsInstall covers a first `telepresence helm install`.
	VerbsInstall Verbs = "install"
	// VerbsLifecycle also covers `telepresence helm upgrade` and
	// `telepresence helm uninstall`, with their hooks.
	VerbsLifecycle Verbs = "lifecycle"
)

// ParseVerbs parses the value of a --verbs flag.
func ParseVerbs(s string) (Verbs, error) {
	switch v := Verbs(s); v {
	case VerbsInstall, VerbsLifecycle:
		return v, nil
	default:
		return "", fmt.Errorf("invalid verbs %q, must be %q or %q", s, VerbsInstall, VerbsLifecycle)
	}
}

const rbacGroup = rbac.GroupName

// RoleNeed is a Role or ClusterRole of the chart. Creating and binding it
// requires holding every one of its Rules, unless the caller holds every
// attribute in Waiver, which is escalate and bind on the role itself.
type RoleNeed struct {
	Rules  []*auth.ResourceAttributes
	Waiver []*auth.ResourceAttributes
}

// InstallSet is what a Helm install of the chart asks of the API server.
type InstallSet struct {
	Attributes []*auth.ResourceAttributes
	Roles      []RoleNeed
}

// HelmInstallSet returns what `telepresence helm install` needs for render
// in managerNamespace: creating the objects, Helm's reads before and after
// the create, the lookups in the templates, the release storage, the deletes
// of a rollback, and the rules of the chart's own roles. VerbsLifecycle adds
// what upgrades and uninstalls need, including the hook objects.
// The values must include the chart defaults.
func HelmInstallSet(render *Render, managerNamespace string, values *helm.Values, verbs Verbs) (*InstallSet, error) {
	objs := render.Objects

	// telepresence helm install creates the namespace by default, and the
	// API server authorizes that before it knows whether it exists.
	ras := []*auth.ResourceAttributes{createNamespaceAttribute()}
	for _, verb := range []string{"create", "get", "delete"} {
		ras = append(ras, VerbAttributes(verb, objs, managerNamespace)...)
	}
	ras = append(ras, releaseStorageAttributes(managerNamespace, verbs)...)
	ras = append(ras, nodeAgentAttributes(managerNamespace, values)...)
	if slices.ContainsFunc(objs, func(o *unstructured.Unstructured) bool { return o.GetKind() == "MutatingWebhookConfiguration" }) {
		ras = append(ras, lookupAttributes(managerNamespace, values)...)
	}

	roleObjs := objs
	if verbs == VerbsLifecycle {
		hooks := lifecycleHooks(render.Hooks)
		for _, verb := range []string{"update", "patch"} {
			ras = append(ras, VerbAttributes(verb, objs, managerNamespace)...)
		}
		for _, verb := range []string{"create", "get", "delete"} {
			ras = append(ras, VerbAttributes(verb, hooks, managerNamespace)...)
		}
		ras = append(ras, hookWatchAttributes(hooks, managerNamespace)...)
		roleObjs = append(slices.Clone(objs), hooks...)
	}

	roles, binds, err := escalationNeeds(roleObjs, managerNamespace)
	if err != nil {
		return nil, err
	}
	return &InstallSet{Attributes: DedupeAttributes(append(ras, binds...)), Roles: roles}, nil
}

// All returns every attribute of the set, with the rules of every role.
func (s *InstallSet) All() []*auth.ResourceAttributes {
	all := slices.Clone(s.Attributes)
	for _, r := range s.Roles {
		all = append(all, r.Rules...)
	}
	return all
}

// Sweep reviews the set for the caller and returns what is denied, sorted
// by AttributeKey. A role's rules are left out when its waiver passes, and
// the waivers themselves are never reported.
func (s *InstallSet) Sweep(ctx context.Context, client kubernetes.Interface) ([]*auth.ResourceAttributes, error) {
	all := slices.Clone(s.Attributes)
	for _, r := range s.Roles {
		all = append(all, r.Rules...)
		all = append(all, r.Waiver...)
	}
	denied, err := Sweep(ctx, client, all)
	if err != nil {
		return nil, err
	}
	isDenied := make(map[*auth.ResourceAttributes]bool, len(denied))
	for _, ra := range denied {
		isDenied[ra] = true
	}
	drop := make(map[*auth.ResourceAttributes]bool)
	for _, r := range s.Roles {
		waived := true
		for _, w := range r.Waiver {
			drop[w] = true
			if isDenied[w] {
				waived = false
			}
		}
		if waived {
			for _, ra := range r.Rules {
				drop[ra] = true
			}
		}
	}
	// The rules of the roles repeat attributes of the set and of each other.
	return DedupeAttributes(slices.DeleteFunc(denied, func(ra *auth.ResourceAttributes) bool { return drop[ra] })), nil
}

// releaseSecretName is the name of the first revision of the release in
// Helm's secrets storage.
const releaseSecretName = "sh.helm.release.v1." + ReleaseName + ".v1"

// releaseStorageAttributes covers Helm's secrets storage. Revisions carry
// their number in the name, so only the delete of the first one, which the
// rollback of a failed install does, can be named.
func releaseStorageAttributes(managerNamespace string, verbs Verbs) []*auth.ResourceAttributes {
	var ras []*auth.ResourceAttributes
	for _, verb := range []string{"list", "get", "create", "update"} {
		ras = append(ras, &auth.ResourceAttributes{Verb: verb, Resource: "secrets", Namespace: managerNamespace})
	}
	if verbs == VerbsLifecycle {
		return append(ras, &auth.ResourceAttributes{Verb: "delete", Resource: "secrets", Namespace: managerNamespace})
	}
	return append(ras, &auth.ResourceAttributes{Verb: "delete", Resource: "secrets", Namespace: managerNamespace, Name: releaseSecretName})
}

// lookupAttributes covers the lookup calls in agentInjectorWebhook.yaml:
// all namespaces, the traffic-manager ConfigMap in each of them, and the
// webhook secret.
func lookupAttributes(managerNamespace string, values *helm.Values) []*auth.ResourceAttributes {
	ras := []*auth.ResourceAttributes{
		{Verb: "list", Resource: "namespaces"},
		{Verb: "get", Resource: "configmaps", Name: ReleaseName},
	}
	if name := webhookSecretName(values); name != "" {
		ras = append(ras, &auth.ResourceAttributes{Verb: "get", Resource: "secrets", Namespace: managerNamespace, Name: name})
	}
	return ras
}

func webhookSecretName(values *helm.Values) string {
	if n := values.AgentInjector.Secret.Name; n != nil {
		return *n
	}
	return ""
}

// lifecycleHooks returns the hooks that upgrades and uninstalls run, which
// leaves out the test hooks.
func lifecycleHooks(hooks []*unstructured.Unstructured) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, h := range hooks {
		for ev := range strings.SplitSeq(h.GetAnnotations()["helm.sh/hook"], ",") {
			if ev = strings.TrimSpace(ev); ev != "" && !strings.HasPrefix(ev, "test") {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

// hookWatchAttributes covers Helm's wait for a hook Job, which lists and
// watches the Job by name.
func hookWatchAttributes(hooks []*unstructured.Unstructured, managerNamespace string) []*auth.ResourceAttributes {
	var jobs []*unstructured.Unstructured
	for _, h := range hooks {
		if h.GetKind() == "Job" {
			jobs = append(jobs, h)
		}
	}
	return append(VerbAttributes("list", jobs, managerNamespace), VerbAttributes("watch", jobs, managerNamespace)...)
}

// escalationNeeds returns a RoleNeed for every Role and ClusterRole in objs,
// and a bind attribute for every binding whose role isn't one of them.
func escalationNeeds(objs []*unstructured.Unstructured, managerNamespace string) ([]RoleNeed, []*auth.ResourceAttributes, error) {
	chartRoles := make(map[string]bool)
	var roles []RoleNeed
	for _, obj := range objs {
		kind := obj.GetKind()
		if kind != "Role" && kind != "ClusterRole" {
			continue
		}
		ns := ObjectNamespace(obj, managerNamespace)
		chartRoles[roleKey(kind, ns, obj.GetName())] = true
		rules, err := ruleAttributes(obj, ns)
		if err != nil {
			return nil, nil, err
		}
		resource := roleResource(kind)
		roles = append(roles, RoleNeed{
			Rules: rules,
			Waiver: []*auth.ResourceAttributes{
				{Verb: "escalate", Group: rbacGroup, Resource: resource, Namespace: ns, Name: obj.GetName()},
				{Verb: "bind", Group: rbacGroup, Resource: resource, Namespace: ns, Name: obj.GetName()},
			},
		})
	}

	var binds []*auth.ResourceAttributes
	for _, obj := range objs {
		kind := obj.GetKind()
		if kind != "RoleBinding" && kind != "ClusterRoleBinding" {
			continue
		}
		refKind, _, _ := unstructured.NestedString(obj.Object, "roleRef", "kind")
		refName, _, _ := unstructured.NestedString(obj.Object, "roleRef", "name")
		ns := ObjectNamespace(obj, managerNamespace)
		refNs := ns
		if refKind == "ClusterRole" {
			refNs = ""
		}
		if chartRoles[roleKey(refKind, refNs, refName)] {
			continue
		}
		binds = append(binds, &auth.ResourceAttributes{
			Verb: "bind", Group: rbacGroup, Resource: roleResource(refKind), Namespace: ns, Name: refName,
		})
	}
	return roles, binds, nil
}

func roleKey(kind, ns, name string) string {
	return kind + "|" + ns + "|" + name
}

func roleResource(kind string) string {
	if kind == "ClusterRole" {
		return "clusterroles"
	}
	return "roles"
}

// ruleAttributes expands the rules of a Role or ClusterRole into one
// attribute per group, resource, verb and name, in namespace ns.
func ruleAttributes(obj *unstructured.Unstructured, ns string) ([]*auth.ResourceAttributes, error) {
	var role rbac.ClusterRole
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &role); err != nil {
		return nil, fmt.Errorf("unable to decode %s %s: %w", obj.GetKind(), obj.GetName(), err)
	}
	var ras []*auth.ResourceAttributes
	for _, rule := range role.Rules {
		names := rule.ResourceNames
		if len(names) == 0 {
			names = []string{""}
		}
		for _, group := range rule.APIGroups {
			for _, res := range rule.Resources {
				resource, sub, _ := strings.Cut(res, "/")
				for _, verb := range rule.Verbs {
					for _, name := range names {
						ras = append(ras, &auth.ResourceAttributes{
							Verb: verb, Group: group, Resource: resource, Subresource: sub, Namespace: ns, Name: name,
						})
					}
				}
			}
		}
	}
	return ras, nil
}

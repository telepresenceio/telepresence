package rbacgen

import (
	"bytes"
	"slices"
	"sort"
	"strings"

	auth "k8s.io/api/authorization/v1"
	rbac "k8s.io/api/rbac/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// PolicyRules turns attributes into rules, one per group, resource and set
// of names, with the verbs that share them. A create rule never carries
// names, because the API server never matches one against a create, and a
// verb that is needed without a name loses its names.
func PolicyRules(ras []*auth.ResourceAttributes) []rbac.PolicyRule {
	type target struct{ group, resource, verb string }
	names := make(map[target]map[string]bool)
	for _, ra := range ras {
		res := ra.Resource
		if ra.Subresource != "" {
			res += "/" + ra.Subresource
		}
		t := target{ra.Group, res, ra.Verb}
		ns, ok := names[t]
		if !ok {
			ns = make(map[string]bool)
			names[t] = ns
		}
		if ra.Name == "" || ra.Verb == "create" {
			ns[""] = true
		} else {
			ns[ra.Name] = true
		}
	}

	type ruleKey struct{ group, resource, names string }
	verbs := make(map[ruleKey][]string)
	for t, ns := range names {
		var list []string
		if !ns[""] {
			for n := range ns {
				list = append(list, n)
			}
			sort.Strings(list)
		}
		k := ruleKey{t.group, t.resource, strings.Join(list, ",")}
		verbs[k] = append(verbs[k], t.verb)
	}

	rules := make([]rbac.PolicyRule, 0, len(verbs))
	for k, vs := range verbs {
		sort.Strings(vs)
		rule := rbac.PolicyRule{APIGroups: []string{k.group}, Resources: []string{k.resource}, Verbs: vs}
		if k.names != "" {
			rule.ResourceNames = strings.Split(k.names, ",")
		}
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool {
		a, b := rules[i], rules[j]
		if a.APIGroups[0] != b.APIGroups[0] {
			return a.APIGroups[0] < b.APIGroups[0]
		}
		if a.Resources[0] != b.Resources[0] {
			return a.Resources[0] < b.Resources[0]
		}
		if an, bn := strings.Join(a.ResourceNames, ","), strings.Join(b.ResourceNames, ","); an != bn {
			return an < bn
		}
		return strings.Join(a.Verbs, ",") < strings.Join(b.Verbs, ",")
	})
	return rules
}

// Manifests returns a ClusterRole and ClusterRoleBinding for the attributes
// without a namespace, and a Role and RoleBinding for each namespace, all
// named name and bound to subjects. The documents are sorted, cluster scope
// first, and an empty attribute list gives no documents.
func Manifests(name string, subjects []rbac.Subject, ras []*auth.ResourceAttributes) ([]byte, error) {
	byNs := make(map[string][]*auth.ResourceAttributes)
	for _, ra := range ras {
		byNs[ra.Namespace] = append(byNs[ra.Namespace], ra)
	}
	namespaces := make([]string, 0, len(byNs))
	for ns := range byNs {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)

	var docs []any
	for _, ns := range namespaces {
		rules := PolicyRules(byNs[ns])
		if ns == "" {
			docs = append(docs,
				&rbac.ClusterRole{
					TypeMeta:   typeMeta("ClusterRole"),
					ObjectMeta: meta.ObjectMeta{Name: name},
					Rules:      rules,
				},
				&rbac.ClusterRoleBinding{
					TypeMeta:   typeMeta("ClusterRoleBinding"),
					ObjectMeta: meta.ObjectMeta{Name: name},
					RoleRef:    rbac.RoleRef{APIGroup: rbacGroup, Kind: "ClusterRole", Name: name},
					Subjects:   slices.Clone(subjects),
				})
			continue
		}
		docs = append(docs,
			&rbac.Role{
				TypeMeta:   typeMeta("Role"),
				ObjectMeta: meta.ObjectMeta{Name: name, Namespace: ns},
				Rules:      rules,
			},
			&rbac.RoleBinding{
				TypeMeta:   typeMeta("RoleBinding"),
				ObjectMeta: meta.ObjectMeta{Name: name, Namespace: ns},
				RoleRef:    rbac.RoleRef{APIGroup: rbacGroup, Kind: "Role", Name: name},
				Subjects:   slices.Clone(subjects),
			})
	}

	var buf bytes.Buffer
	for _, doc := range docs {
		data, err := yaml.Marshal(doc)
		if err != nil {
			return nil, err
		}
		buf.WriteString("---\n")
		buf.Write(data)
	}
	return buf.Bytes(), nil
}

func typeMeta(kind string) meta.TypeMeta {
	return meta.TypeMeta{APIVersion: rbac.SchemeGroupVersion.String(), Kind: kind}
}

package golden

import (
	"io"
	"slices"
	"strings"
	"testing"

	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const (
	managerClusterScopeTpl   = "telepresence-oss/templates/trafficManagerRbac/cluster-scope.yaml"
	managerNamespaceScopeTpl = "telepresence-oss/templates/trafficManagerRbac/namespace-scope.yaml"
)

// workloadRuleVerbs returns, per resource, the verbs of the apps-group
// workload rules of every Role/ClusterRole rendered by tpl.
func workloadRuleVerbs(t *testing.T, tpl, doc string) map[string][][]string {
	t.Helper()
	res := map[string][][]string{}
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(doc), 4096)
	for {
		var role rbac.ClusterRole
		err := decoder.Decode(&role)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode %s: %v", tpl, err)
		}
		if role.Kind != "ClusterRole" && role.Kind != "Role" {
			continue
		}
		for _, r := range role.Rules {
			if !slices.Contains(r.APIGroups, "apps") {
				continue
			}
			for _, resource := range r.Resources {
				if slices.Contains([]string{"deployments", "replicasets", "statefulsets"}, resource) {
					res[resource] = append(res[resource], r.Verbs)
				}
			}
		}
	}
	return res
}

// TestManagerWorkloadPatchVerb asserts that the traffic-manager's workload
// rules grant patch only when the agent injector is enabled and
// managerRbac.patchWorkloads is not false, in both cluster and namespace scope.
func TestManagerWorkloadPatchVerb(t *testing.T) {
	scopes := []struct {
		name string
		tpl  string
		vals map[string]any
		// roles is the number of Role/ClusterRole documents expected per resource.
		roles int
	}{
		{"cluster", managerClusterScopeTpl, map[string]any{}, 1},
		// A Role for each listed namespace plus the manager's own.
		{"namespaced", managerNamespaceScopeTpl, map[string]any{"namespaces": []string{"ns-a", "ns-b"}}, 3},
	}
	cases := []struct {
		name      string
		overrides map[string]any
		wantPatch bool
	}{
		{"default", map[string]any{}, true},
		{"patchWorkloads-false", map[string]any{"managerRbac": map[string]any{"patchWorkloads": false}}, false},
		{"injector-disabled", map[string]any{"agentInjector": map[string]any{"enabled": false}}, false},
	}
	for _, sc := range scopes {
		for _, tc := range cases {
			t.Run(sc.name+"/"+tc.name, func(t *testing.T) {
				vals := map[string]any{}
				for k, v := range sc.vals {
					vals[k] = v
				}
				for k, v := range tc.overrides {
					vals[k] = v
				}
				out := renderChart(t, vals)
				rules := workloadRuleVerbs(t, sc.tpl, out[sc.tpl])
				for _, resource := range []string{"deployments", "replicasets", "statefulsets"} {
					all := rules[resource]
					if len(all) != sc.roles {
						t.Errorf("%s: got %d rules, want %d", resource, len(all), sc.roles)
					}
					for _, verbs := range all {
						for _, v := range []string{"get", "list", "watch"} {
							if !slices.Contains(verbs, v) {
								t.Errorf("%s: verb %q missing from %v", resource, v, verbs)
							}
						}
						if got := slices.Contains(verbs, "patch"); got != tc.wantPatch {
							t.Errorf("%s: patch granted = %t, want %t (verbs %v)", resource, got, tc.wantPatch, verbs)
						}
					}
				}
			})
		}
	}
}

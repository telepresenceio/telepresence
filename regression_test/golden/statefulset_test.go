package golden

import (
	"strconv"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

// renderChartAs is renderChart with a caller-chosen release name/namespace,
// for the pod-name contract test below: renderChart's releaseName constant
// is fixed, but this test needs to vary it.
func renderChartAs(t *testing.T, vals map[string]any, relName, relNamespace string) map[string]string {
	t.Helper()
	chrt, err := loadChart()
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	rv, err := chartutil.ToRenderValues(chrt, vals,
		chartutil.ReleaseOptions{Name: relName, Namespace: relNamespace, IsInstall: true},
		chartutil.DefaultCapabilities)
	if err != nil {
		t.Fatalf("coalesce values: %v", err)
	}
	out, err := (engine.Engine{}).Render(chrt, rv)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return out
}

// renderErr is renderChart but returns a render/coalesce error instead of
// failing the test, for the replicaCount>1 guard below.
func renderErr(t *testing.T, vals map[string]any) error {
	t.Helper()
	chrt, err := loadChart()
	if err != nil {
		t.Fatalf("load chart: %v", err)
	}
	rv, err := chartutil.ToRenderValues(chrt, vals,
		chartutil.ReleaseOptions{Name: releaseName, Namespace: releaseNamespace, IsInstall: true},
		chartutil.DefaultCapabilities)
	if err != nil {
		return err
	}
	_, err = (engine.Engine{}).Render(chrt, rv)
	return err
}

// TestPodNameContract pins that the StatefulSet is always named
// "traffic-manager" -- the client's known-name pods/portforward connect
// path dials pod traffic-manager-0 directly, so the helper that names the
// workload (traffic-manager.name in _helpers.tpl) must never start honoring
// nameOverride or the release name. This chart has no fullnameOverride
// value to vary; nameOverride and the release name are its only naming
// inputs.
func TestPodNameContract(t *testing.T) {
	cases := []struct {
		name    string
		vals    map[string]any
		relName string
	}{
		{"defaults", map[string]any{}, releaseName},
		{"nameOverride", map[string]any{"nameOverride": "rtest-custom-name"}, releaseName},
		{"releaseName", map[string]any{}, "rtest-custom-release"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderChartAs(t, c.vals, c.relName, releaseNamespace)
			doc := out[statefulsetTpl]
			if !strings.Contains(doc, "\n  name: traffic-manager\n") {
				t.Errorf("%s: StatefulSet name is not traffic-manager under %s:\n%s", statefulsetTpl, c.name, doc)
			}
			if !strings.Contains(doc, "\n  serviceName: traffic-manager-headless") {
				t.Errorf("%s: serviceName did not resolve to traffic-manager-headless under %s:\n%s", statefulsetTpl, c.name, doc)
			}
		})
	}
}

// TestReplicaCountRejected asserts that replicaCount > 1 fails the render
// unconditionally -- the manager is a singleton (CA and session state are
// process-local) and the known-name connect path assumes exactly one pod,
// traffic-manager-0.
func TestReplicaCountRejected(t *testing.T) {
	err := renderErr(t, map[string]any{"replicaCount": 2})
	if err == nil {
		t.Fatalf("expected render to fail for replicaCount=2")
	}
	if !strings.Contains(err.Error(), "replicaCount must be 1") {
		t.Errorf("unexpected render error: %v", err)
	}
}

// TestPreUpgradeMigrationHook asserts the pre-upgrade hook Job that deletes
// a pre-StatefulSet Deployment named traffic-manager renders along with its
// scoped ServiceAccount/Role/RoleBinding. engine.Render (used by
// renderChart) has no notion of Helm's hook lifecycle -- it renders every
// template unconditionally -- so the "helm.sh/hook": pre-upgrade annotation
// is what makes a real `helm upgrade` skip this Job on install, not a
// template-level IsInstall guard.
func TestPreUpgradeMigrationHook(t *testing.T) {
	out := renderChart(t, map[string]any{})
	if !rendered(out, preUpgradeHookTpl) {
		t.Fatalf("%s did not render", preUpgradeHookTpl)
	}
	doc := out[preUpgradeHookTpl]
	for _, want := range []string{
		`"helm.sh/hook": pre-upgrade`,
		"kind: ServiceAccount",
		"kind: Role",
		"kind: RoleBinding",
		"kind: Job",
		`resourceNames: ["traffic-manager"]`,
		"serviceAccountName: traffic-manager-migrate-hook",
		// Foreground propagation makes polling the Deployment to 404 double
		// as the pod-termination wait, and the 404 fast path is what keeps
		// every post-migration upgrade from waiting on the live StatefulSet
		// pod (same selector labels as the old Deployment's pods).
		"propagationPolicy=Foreground",
		"404) exit 0",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("%s: %q not rendered", preUpgradeHookTpl, want)
		}
	}
}

// TestClientRbacLegacyAccess asserts clientRbac.legacyAccess's two
// renderings: true (the chart default) keeps the discovery and diagnostic
// grants that a pre-2.33 client needs; false reduces the connect Role to the
// single name-scoped pods/portforward rule and drops the legacy grants from
// the cluster-scope ClusterRole, leaving only the gate-driven and
// gate-independent telepresence.io rules.
func TestClientRbacLegacyAccess(t *testing.T) {
	subjects := []map[string]any{{
		"kind":      "ServiceAccount",
		"name":      "rtest-golden",
		"namespace": releaseNamespace,
	}}

	t.Run("false", func(t *testing.T) {
		out := renderChart(t, map[string]any{
			"clientRbac": map[string]any{
				"create":       true,
				"legacyAccess": false,
				"subjects":     subjects,
			},
		})
		connectDoc := out[clientConnectTpl]
		if !strings.Contains(connectDoc, "resourceNames:\n      - traffic-manager-0") {
			t.Errorf("%s: name-scoped pods/portforward rule missing:\n%s", clientConnectTpl, connectDoc)
		}
		for _, absent := range []string{`resources: ["pods"]`, `resources: ["services"]`} {
			if strings.Contains(connectDoc, absent) {
				t.Errorf("%s: %q rendered under legacyAccess=false", clientConnectTpl, absent)
			}
		}

		clusterDoc := out[clientClusterScopeTpl]
		for _, absent := range []string{`resources: ["namespaces"]`, `resources: ["pods"]`, `resources: ["pods/log"]`} {
			if strings.Contains(clusterDoc, absent) {
				t.Errorf("%s: %q rendered under legacyAccess=false", clientClusterScopeTpl, absent)
			}
		}
		// The telepresence.io logs/logs-yaml diagnostic grant is
		// unaffected by legacyAccess -- it's the phase-2 StreamLogs
		// authorization, not the legacy direct pods/log path.
		if !strings.Contains(clusterDoc, `resources: ["logs", "logs/yaml"]`) {
			t.Errorf("%s: logs/logs-yaml grant missing under legacyAccess=false", clientClusterScopeTpl)
		}
	})

	for _, tc := range []struct {
		name string
		vals map[string]any
	}{
		{"true", map[string]any{"clientRbac": map[string]any{"create": true, "legacyAccess": true, "subjects": subjects}}},
		{"default", map[string]any{"clientRbac": map[string]any{"create": true, "subjects": subjects}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderChart(t, tc.vals)
			connectDoc := out[clientConnectTpl]
			for _, want := range []string{`resources: ["pods"]`, `resources: ["services"]`, `resources: ["pods/portforward"]`} {
				if !strings.Contains(connectDoc, want) {
					t.Errorf("%s: %q missing under legacyAccess=%s", clientConnectTpl, want, tc.name)
				}
			}
			if strings.Contains(connectDoc, "resourceNames:\n      - traffic-manager-0") {
				t.Errorf("%s: name-scoped pods/portforward rule rendered under legacyAccess=%s", clientConnectTpl, tc.name)
			}

			clusterDoc := out[clientClusterScopeTpl]
			for _, want := range []string{`resources: ["namespaces"]`, `resources: ["pods"]`, `resources: ["pods/log"]`} {
				if !strings.Contains(clusterDoc, want) {
					t.Errorf("%s: %q missing under legacyAccess=%s", clientClusterScopeTpl, want, tc.name)
				}
			}
		})
	}
}

// TestClientRbacGateLegacyAccessCrossProduct asserts the connect Role's
// transport-vs-policy invariant over the full {gate, legacyAccess}
// cross-product: the mechanical pods/portforward transport rule renders for
// every gate value regardless of legacyAccess (scoped to traffic-manager-0
// when false, unscoped alongside the discovery rules when true), while the
// telepresence.io connections rule is purely gate-driven -- present iff
// gate != "portforward" -- in both legacyAccess states.
func TestClientRbacGateLegacyAccessCrossProduct(t *testing.T) {
	subjects := []map[string]any{{
		"kind":      "ServiceAccount",
		"name":      "rtest-golden",
		"namespace": releaseNamespace,
	}}
	for _, legacyAccess := range []bool{true, false} {
		for _, gate := range []string{"portforward", "telepresence", "any"} {
			name := "gate=" + gate + "/legacyAccess=" + strconv.FormatBool(legacyAccess)
			t.Run(name, func(t *testing.T) {
				out := renderChart(t, map[string]any{
					"clientRbac": map[string]any{
						"create":       true,
						"legacyAccess": legacyAccess,
						"subjects":     subjects,
					},
					"security": map[string]any{
						"authorization": map[string]any{"gate": gate},
					},
				})
				connectDoc := out[clientConnectTpl]

				// Transport rule: present under every gate value, in both
				// toggle states, just scoped differently.
				if !strings.Contains(connectDoc, `resources: ["pods/portforward"]`) {
					t.Errorf("%s: pods/portforward rule missing under %s", clientConnectTpl, name)
				}
				nameScoped := strings.Contains(connectDoc, "resourceNames:\n      - traffic-manager-0")
				if legacyAccess {
					if nameScoped {
						t.Errorf("%s: name-scoped rule rendered under %s", clientConnectTpl, name)
					}
					for _, want := range []string{`resources: ["pods"]`, `resources: ["services"]`} {
						if !strings.Contains(connectDoc, want) {
							t.Errorf("%s: %q missing under %s", clientConnectTpl, want, name)
						}
					}
				} else {
					if !nameScoped {
						t.Errorf("%s: name-scoped pods/portforward rule missing under %s", clientConnectTpl, name)
					}
					for _, absent := range []string{`resources: ["pods"]`, `resources: ["services"]`} {
						if strings.Contains(connectDoc, absent) {
							t.Errorf("%s: %q rendered under %s", clientConnectTpl, absent, name)
						}
					}
				}

				// Policy rule: gate-driven only, unaffected by legacyAccess.
				wantConn := gate != "portforward"
				if got := strings.Contains(connectDoc, `resources: ["connections"]`); got != wantConn {
					t.Errorf("%s: connections rule present=%v, want %s -> %v", clientConnectTpl, got, name, wantConn)
				}
			})
		}
	}
}

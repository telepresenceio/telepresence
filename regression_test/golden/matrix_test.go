package golden

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// Axis names double as the combo's map key and the dotted chart-values path
// they configure, so valuesFromCombo can build the nested values map
// generically instead of a per-axis switch.
const (
	axisInjectorEnabled = "agentInjector.enabled"
	axisInjectPolicy    = "agentInjector.injectPolicy"
	axisNodeAgent       = "nodeAgent.enabled"
	axisQuicTunnel      = "quicTunnel.enabled"
	axisAuthMode        = "security.authentication.mode"
	axisAuthGate        = "security.authorization.gate"
	axisAPIPort         = "telepresenceAPI.port"
	axisUsageEnabled    = "usage.enabled"
)

// matrixAxes are the manager-value dimensions this tier renders, drawn from
// the same chart keys the managers catalog (framework/managers) configures
// live installs with.
func matrixAxes() []rt.Axis {
	return []rt.Axis{
		{Name: axisInjectorEnabled, Values: []string{"true", "false"}},
		{Name: axisInjectPolicy, Values: []string{"OnDemand", "WhenEnabled"}},
		{Name: axisNodeAgent, Values: []string{"true", "false"}},
		{Name: axisQuicTunnel, Values: []string{"true", "false"}},
		{Name: axisAuthMode, Values: []string{"permissive", "enforcing"}},
		{Name: axisAuthGate, Values: []string{"portforward", "telepresence", "any"}},
		{Name: axisAPIPort, Values: []string{"0", "9980"}},
		{Name: axisUsageEnabled, Values: []string{"false", "true"}},
	}
}

// valuesFromCombo builds the chart values overlay for one Pairwise combo.
// Every axis in matrixAxes has a fixed, known type (bool, string, or int),
// so this is a direct field-by-field conversion rather than generic
// dotted-path parsing.
func valuesFromCombo(c map[string]string) map[string]any {
	port, err := strconv.Atoi(c[axisAPIPort])
	if err != nil {
		panic("bad " + axisAPIPort + " value: " + c[axisAPIPort]) // combo values come from matrixAxes above
	}
	return map[string]any{
		"agentInjector": map[string]any{
			"enabled":      c[axisInjectorEnabled] == "true",
			"injectPolicy": c[axisInjectPolicy],
		},
		"nodeAgent":  map[string]any{"enabled": c[axisNodeAgent] == "true"},
		"quicTunnel": map[string]any{"enabled": c[axisQuicTunnel] == "true"},
		"security": map[string]any{
			"authentication": map[string]any{"mode": c[axisAuthMode]},
			"authorization":  map[string]any{"gate": c[axisAuthGate]},
		},
		"telepresenceAPI": map[string]any{"port": port},
		"usage":           map[string]any{"enabled": c[axisUsageEnabled] == "true"},
		// clientRbac isn't itself an axis (its shape doesn't vary with the
		// combo), but it must be enabled for the assertions below to see the
		// client Role content the gate axis controls.
		"clientRbac": map[string]any{
			"create": true,
			"subjects": []map[string]any{{
				"kind":      "ServiceAccount",
				"name":      "rtest-golden",
				"namespace": releaseNamespace,
			}},
		},
	}
}

// envLineRE matches one `- name: KEY` / `value: VAL` pair from a rendered
// Deployment's env list (deployment.yaml's own indentation and quoting).
// Entries using `valueFrom:` (MANAGER_NAMESPACE, POD_IP, POD_HOST_IP) don't
// match: the line after `- name:` isn't `value:`, so they're silently
// skipped rather than mismatched.
var envLineRE = regexp.MustCompile(`(?m)^\s*- name:\s*(\S+)\n\s*value:\s*"?([^"\n]*?)"?\s*$`)

// parseEnv extracts the container env vars set via plain `value:` (not
// `valueFrom:`) from a rendered deployment.yaml, keyed by name.
func parseEnv(deploymentYAML string) map[string]string {
	out := map[string]string{}
	for _, m := range envLineRE.FindAllStringSubmatch(deploymentYAML, -1) {
		out[m[1]] = m[2]
	}
	return out
}

// TestChartMatrix renders the local chart source over a pairwise matrix of
// matrixAxes and asserts structural invariants: no combination in the
// matrix hits a template guard (fail/required), and the axes that gate a
// resource's existence or an env var's value do so consistently.
func TestChartMatrix(t *testing.T) {
	axes := matrixAxes()
	combos := rt.Pairwise(axes, nil)
	full := 1
	for _, a := range axes {
		full *= len(a.Values)
	}
	t.Logf("pairwise: %d combinations (full product would be %d)", len(combos), full)

	for _, c := range combos {
		t.Run(rt.ComboName(c), func(t *testing.T) {
			injectorEnabled := c[axisInjectorEnabled] == "true"
			nodeAgentEnabled := c[axisNodeAgent] == "true"
			quicEnabled := c[axisQuicTunnel] == "true"
			authMode := c[axisAuthMode]
			usageEnabled := c[axisUsageEnabled] == "true"

			out := renderChart(t, valuesFromCombo(c))

			// The deployment renders unconditionally.
			if !rendered(out, deploymentTpl) {
				t.Fatalf("%s did not render", deploymentTpl)
			}

			// The webhook config renders iff the injector is enabled.
			if got := rendered(out, webhookTpl); got != injectorEnabled {
				t.Errorf("%s rendered=%v, want agentInjector.enabled=%v", webhookTpl, got, injectorEnabled)
			}

			// The quic-forwarder workload renders iff quicTunnel is enabled.
			if got := rendered(out, quicFwdTpl); got != quicEnabled {
				t.Errorf("%s rendered=%v, want quicTunnel.enabled=%v", quicFwdTpl, got, quicEnabled)
			}

			// The node-agent RBAC (Role/RoleBinding for node-agent Jobs)
			// renders iff nodeAgent is enabled.
			if got := rendered(out, nodeAgentTpl); got != nodeAgentEnabled {
				t.Errorf("%s rendered=%v, want nodeAgent.enabled=%v", nodeAgentTpl, got, nodeAgentEnabled)
			}

			// The x509 auth kube-system RoleBinding renders iff auth mode
			// is "enforcing" (x509.enabled keeps its chart default of true
			// for every combo in this matrix).
			if got := rendered(out, x509Tpl); got != (authMode == "enforcing") {
				t.Errorf("%s rendered=%v, want security.authentication.mode=enforcing", x509Tpl, got)
			}

			env := parseEnv(out[deploymentTpl])

			// AUTHENTICATION_MODE always carries the configured mode.
			if v := env["AUTHENTICATION_MODE"]; v != authMode {
				t.Errorf("AUTHENTICATION_MODE = %q, want %q", v, authMode)
			}

			// AGENT_INJECT_POLICY exists, and carries the configured
			// policy, iff the injector is enabled (its env block is
			// guarded by the same condition as the webhook config).
			policy, ok := env["AGENT_INJECT_POLICY"]
			if ok != injectorEnabled {
				t.Errorf("AGENT_INJECT_POLICY present=%v, want agentInjector.enabled=%v", ok, injectorEnabled)
			} else if injectorEnabled && policy != c[axisInjectPolicy] {
				t.Errorf("AGENT_INJECT_POLICY = %q, want %q", policy, c[axisInjectPolicy])
			}

			// AGENT_REST_API_PORT exists, and carries the configured port,
			// iff the port is non-zero (the template guards it with `if
			// .port`, treating 0 the same as unset).
			portWant := c[axisAPIPort]
			port, ok := env["AGENT_REST_API_PORT"]
			if wantPresent := portWant != "0"; ok != wantPresent {
				t.Errorf("AGENT_REST_API_PORT present=%v, want telepresenceAPI.port=%s", ok, portWant)
			} else if wantPresent && port != portWant {
				t.Errorf("AGENT_REST_API_PORT = %q, want %q", port, portWant)
			}

			// NODE_AGENT_ENABLED exists iff nodeAgent is enabled.
			if _, ok := env["NODE_AGENT_ENABLED"]; ok != nodeAgentEnabled {
				t.Errorf("NODE_AGENT_ENABLED present=%v, want nodeAgent.enabled=%v", ok, nodeAgentEnabled)
			}

			// USAGE_REPORTING_ENABLED always carries the configured flag:
			// unlike the other toggles above, the chart deliberately never
			// omits this env var (its own comment: quoting the value
			// directly so that an explicit enabled=false isn't coerced
			// back to true), so "present iff enabled" doesn't apply here
			// -- the assertion that does apply is the *value*.
			if v := env["USAGE_REPORTING_ENABLED"]; v != strconv.FormatBool(usageEnabled) {
				t.Errorf("USAGE_REPORTING_ENABLED = %q, want %q", v, strconv.FormatBool(usageEnabled))
			}

			// AUTHORIZATION_GATE always carries the configured gate.
			gate := c[axisAuthGate]
			if v := env["AUTHORIZATION_GATE"]; v != gate {
				t.Errorf("AUTHORIZATION_GATE = %q, want %q", v, gate)
			}

			// LOG_STREAM_* always carry the chart's built-in defaults here:
			// no combo in this matrix overrides logStreaming, so every
			// render exercises the "block absent" defaulting path.
			wantLogStreamEnv := map[string]string{
				"LOG_STREAM_CHUNK_SIZE":      "64Ki",
				"LOG_STREAM_POD_CONCURRENCY": "4",
				"LOG_STREAM_POD_BYTE_LIMIT":  "10Mi",
				"LOG_STREAM_DEADLINE":        "5m",
			}
			for name, want := range wantLogStreamEnv {
				if v := env[name]; v != want {
					t.Errorf("%s = %q, want %q", name, v, want)
				}
			}

			assertClientRoleRules(t, out, gate)
		})
	}
}

// assertClientRoleRules checks the gate-dependent client Role rendering. The
// connect Role's discovery and port-forward rules are the client's only
// transport to the manager and render for every gate value; the gate only
// adds the telepresence.io connections rule (absent for "portforward"). The
// cluster-scope ClusterRole (rendered here because the matrix's combos never
// set a namespaceSelector or clientRbac.namespaces, so the manager is
// cluster-wide) gates its own pods/portforward vs. attachments rule the same
// way; pods get/list, pods/log get, and the telepresence.io logs/logs-yaml
// diagnostic grant are unaffected by the gate and render identically for
// every value. TestNamespaceScopeRoleGate covers the same rules for
// namespace-scope.yaml's per-namespace Role, which this cluster-wide matrix
// never renders.
func assertClientRoleRules(t *testing.T, out map[string]string, gate string) {
	t.Helper()
	if !rendered(out, clientConnectTpl) {
		t.Fatalf("%s did not render", clientConnectTpl)
	}
	connectDoc := out[clientConnectTpl]
	wantConnect := gate != "portforward"
	if !strings.Contains(connectDoc, `resources: ["pods/portforward"]`) {
		t.Errorf("%s pods/portforward rule missing under gate=%q; the transport rules render for every gate", clientConnectTpl, gate)
	}
	if got := strings.Contains(connectDoc, `resources: ["connections"]`); got != wantConnect {
		t.Errorf("%s connections rule present=%v, want gate=%q -> %v", clientConnectTpl, got, gate, wantConnect)
	}

	if !rendered(out, clientClusterScopeTpl) {
		t.Fatalf("%s did not render", clientClusterScopeTpl)
	}
	assertInterceptRules(t, clientClusterScopeTpl, out[clientClusterScopeTpl], gate)
}

// assertInterceptRules checks the gate-dependent and gate-independent rules
// that telepresence.clientRbacInterceptRules renders into a client Role
// (ClusterRole or namespaced Role) doc.
func assertInterceptRules(t *testing.T, tpl, doc, gate string) {
	t.Helper()
	wantAttach := gate != "portforward"
	if got := strings.Contains(doc, `resources: ["pods/portforward"]`); got != (gate != "telepresence") {
		t.Errorf("%s pods/portforward rule present=%v, want gate=%q -> %v", tpl, got, gate, gate != "telepresence")
	}
	if got := strings.Contains(doc, "attachments/deployments"); got != wantAttach {
		t.Errorf("%s attachments rule present=%v, want gate=%q -> %v", tpl, got, gate, wantAttach)
	}
	for _, want := range []string{`resources: ["pods"]`, `resources: ["pods/log"]`, `resources: ["logs", "logs/yaml"]`} {
		if !strings.Contains(doc, want) {
			t.Errorf("%s: %q not rendered regardless of gate=%q", tpl, want, gate)
		}
	}
}

// TestNamespaceScopeRoleGate asserts the per-namespace Role
// (clientRbac/namespace-scope.yaml) renders the same gate-dependent and
// gate-independent telepresence.clientRbacInterceptRules content as the
// cluster-scope ClusterRole, in particular that the telepresence.io
// logs/logs-yaml diagnostic grant renders for every gate value. Setting
// clientRbac.namespaces makes the manager namespace-scoped (traffic-manager.namespaced),
// which is what makes namespace-scope.yaml render instead of cluster-scope.yaml.
func TestNamespaceScopeRoleGate(t *testing.T) {
	for _, gate := range []string{"portforward", "telepresence", "any"} {
		t.Run(gate, func(t *testing.T) {
			out := renderChart(t, map[string]any{
				"security": map[string]any{
					"authorization": map[string]any{"gate": gate},
				},
				"clientRbac": map[string]any{
					"create":     true,
					"namespaces": []string{"rtest-app"},
					"subjects": []map[string]any{{
						"kind":      "ServiceAccount",
						"name":      "rtest-golden",
						"namespace": releaseNamespace,
					}},
				},
			})
			if !rendered(out, clientNamespaceTpl) {
				t.Fatalf("%s did not render", clientNamespaceTpl)
			}
			if rendered(out, clientClusterScopeTpl) {
				t.Fatalf("%s rendered while clientRbac.namespaces was set; expected namespace-scope only", clientClusterScopeTpl)
			}
			assertInterceptRules(t, clientNamespaceTpl, out[clientNamespaceTpl], gate)
		})
	}
}

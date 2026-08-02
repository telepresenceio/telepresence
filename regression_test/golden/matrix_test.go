package golden

import (
	"regexp"
	"strconv"
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
	axisAPIPort         = "telepresenceAPI.port"
	axisUsageEnabled    = "usage.enabled"
)

// matrixAxes are the manager-value dimensions the plan's "Golden chart
// rendering" tier calls out by name (docs/plans/regression-test-framework/
// plan.md), drawn from the same chart keys the managers catalog
// (framework/managers) configures live installs with.
func matrixAxes() []rt.Axis {
	return []rt.Axis{
		{Name: axisInjectorEnabled, Values: []string{"true", "false"}},
		{Name: axisInjectPolicy, Values: []string{"OnDemand", "WhenEnabled"}},
		{Name: axisNodeAgent, Values: []string{"true", "false"}},
		{Name: axisQuicTunnel, Values: []string{"true", "false"}},
		{Name: axisAuthMode, Values: []string{"permissive", "enforcing"}},
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
		},
		"telepresenceAPI": map[string]any{"port": port},
		"usage":           map[string]any{"enabled": c[axisUsageEnabled] == "true"},
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
		})
	}
}

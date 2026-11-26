package golden

import (
	"strings"
	"testing"
)

// TestAuthModeDefault asserts that a render with no authentication mode set
// yields an enforcing traffic-manager: the env var, the x509 listener port,
// and the kube-system RoleBinding that the listener needs.
func TestAuthModeDefault(t *testing.T) {
	out := renderChart(t, map[string]any{})
	if !rendered(out, statefulsetTpl) {
		t.Fatalf("%s did not render", statefulsetTpl)
	}
	if v := parseEnv(out[statefulsetTpl])["AUTHENTICATION_MODE"]; v != "enforcing" {
		t.Errorf("AUTHENTICATION_MODE = %q, want enforcing", v)
	}
	if !hasX509Listener(out[statefulsetTpl]) {
		t.Errorf("%s: authx509 container port not rendered", statefulsetTpl)
	}
	if !rendered(out, x509Tpl) {
		t.Errorf("%s did not render", x509Tpl)
	}
}

// TestAuthModeExplicitOverrides asserts that an explicit mode replaces the
// default and drops the resources that only exist under enforcing.
func TestAuthModeExplicitOverrides(t *testing.T) {
	for _, mode := range []string{"permissive", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			out := renderChart(t, map[string]any{
				"security": map[string]any{"authentication": map[string]any{"mode": mode}},
			})
			if v := parseEnv(out[statefulsetTpl])["AUTHENTICATION_MODE"]; v != mode {
				t.Errorf("AUTHENTICATION_MODE = %q, want %q", v, mode)
			}
			if hasX509Listener(out[statefulsetTpl]) {
				t.Errorf("%s: authx509 container port rendered under %s", statefulsetTpl, mode)
			}
			if rendered(out, x509Tpl) {
				t.Errorf("%s rendered under %s", x509Tpl, mode)
			}
		})
	}
}

func hasX509Listener(statefulset string) bool {
	return len(statefulset) > 0 && strings.Contains(statefulset, "name: authx509")
}

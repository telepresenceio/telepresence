package golden

import (
	"strings"
	"testing"
)

// TestExternalEndpointDisabledByDefault asserts that a default render (no
// externalEndpoint values) produces no external Service/Certificate, no
// external containerPort or env, and no external-tls volume/mount --
// externalEndpoint.enabled defaults to false.
func TestExternalEndpointDisabledByDefault(t *testing.T) {
	out := renderChart(t, map[string]any{})
	if rendered(out, externalEndpointTpl) {
		t.Fatalf("%s rendered under default values:\n%s", externalEndpointTpl, out[externalEndpointTpl])
	}
	doc := out[statefulsetTpl]
	for _, absent := range []string{"EXTERNAL_PORT", "EXTERNAL_TLS_CERT_DIR", "name: external", "external-tls"} {
		if strings.Contains(doc, absent) {
			t.Errorf("%s: %q rendered under default values", statefulsetTpl, absent)
		}
	}
}

// TestExternalEndpointSecretName asserts that externalEndpoint.enabled with
// an explicit tls.secretName, under enforcing auth mode, renders the
// external Service, the manager's EXTERNAL_PORT/EXTERNAL_TLS_CERT_DIR env
// and containerPort, and a volume/mount sourced from the given Secret.
func TestExternalEndpointSecretName(t *testing.T) {
	out := renderChart(t, map[string]any{
		"security": map[string]any{
			"authentication": map[string]any{"mode": "enforcing"},
		},
		"externalEndpoint": map[string]any{
			"enabled": true,
			"port":    8444,
			"tls": map[string]any{
				"secretName": "rtest-external-tls",
			},
		},
	})
	if !rendered(out, externalEndpointTpl) {
		t.Fatalf("%s did not render", externalEndpointTpl)
	}
	svcDoc := out[externalEndpointTpl]
	for _, want := range []string{
		"kind: Service",
		"name: traffic-manager-external\n",
		"type: LoadBalancer",
		"port: 443",
		"targetPort: external",
	} {
		if !strings.Contains(svcDoc, want) {
			t.Errorf("%s: %q not rendered:\n%s", externalEndpointTpl, want, svcDoc)
		}
	}
	if strings.Contains(svcDoc, "kind: Certificate") {
		t.Errorf("%s: Certificate rendered without tls.certManager.enabled:\n%s", externalEndpointTpl, svcDoc)
	}

	ssDoc := out[statefulsetTpl]
	env := parseEnv(ssDoc)
	if v := env["EXTERNAL_PORT"]; v != "8444" {
		t.Errorf("EXTERNAL_PORT = %q, want 8444", v)
	}
	if v := env["EXTERNAL_TLS_CERT_DIR"]; v != "/var/run/secrets/telepresence.io/external-tls" {
		t.Errorf("EXTERNAL_TLS_CERT_DIR = %q, want /var/run/secrets/telepresence.io/external-tls", v)
	}
	for _, want := range []string{
		"- name: external\n            containerPort: 8444",
		"- name: external-tls\n              mountPath: /var/run/secrets/telepresence.io/external-tls\n              readOnly: true",
		"- name: external-tls\n          secret:\n            secretName: rtest-external-tls",
	} {
		if !strings.Contains(ssDoc, want) {
			t.Errorf("%s: %q not rendered:\n%s", statefulsetTpl, want, ssDoc)
		}
	}
}

// TestExternalEndpointCertManager asserts that
// externalEndpoint.tls.certManager.enabled renders a cert-manager
// Certificate for the traffic-manager-external-tls Secret, with the
// configured dnsNames/issuerRef, and that the StatefulSet's volume falls
// back to that same Secret name.
func TestExternalEndpointCertManager(t *testing.T) {
	out := renderChart(t, map[string]any{
		"security": map[string]any{
			"authentication": map[string]any{"mode": "enforcing"},
		},
		"externalEndpoint": map[string]any{
			"enabled": true,
			"tls": map[string]any{
				"certManager": map[string]any{
					"enabled":   true,
					"dnsNames":  []string{"external.rtest.example.com"},
					"issuerRef": map[string]any{"name": "rtest-issuer", "kind": "ClusterIssuer"},
				},
			},
		},
	})
	doc := out[externalEndpointTpl]
	for _, want := range []string{
		"kind: Certificate",
		"name: traffic-manager-external-tls",
		"secretName: traffic-manager-external-tls",
		"external.rtest.example.com",
		"kind: ClusterIssuer",
		"name: rtest-issuer",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("%s: %q not rendered:\n%s", externalEndpointTpl, want, doc)
		}
	}

	ssDoc := out[statefulsetTpl]
	if !strings.Contains(ssDoc, "secretName: traffic-manager-external-tls") {
		t.Errorf("%s: volume did not fall back to traffic-manager-external-tls:\n%s", statefulsetTpl, ssDoc)
	}
}

// TestExternalEndpointOmitsConnectPortForward asserts that with an external
// endpoint published, the connect Role drops its mechanical pods/portforward
// rule (external clients never port-forward) while keeping the gate-driven
// connections policy grant -- except under gate "portforward", where
// possession of pods/portforward is itself the connect policy, so the named
// grant renders even though nothing exercises it. The Role always carries at
// least one grant the manager's connect review accepts.
func TestExternalEndpointOmitsConnectPortForward(t *testing.T) {
	subjects := []map[string]any{{
		"kind":      "ServiceAccount",
		"name":      "rtest-golden",
		"namespace": releaseNamespace,
	}}
	vals := func(gate string) map[string]any {
		return map[string]any{
			"security": map[string]any{
				"authentication": map[string]any{"mode": "enforcing"},
				"authorization":  map[string]any{"gate": gate},
			},
			"externalEndpoint": map[string]any{
				"enabled": true,
				"tls":     map[string]any{"secretName": "rtest-external-tls"},
			},
			"clientRbac": map[string]any{"create": true, "subjects": subjects},
		}
	}

	t.Run("gate=telepresence", func(t *testing.T) {
		out := renderChart(t, vals("telepresence"))
		if !rendered(out, clientConnectTpl) {
			t.Fatalf("%s did not render", clientConnectTpl)
		}
		doc := out[clientConnectTpl]
		if strings.Contains(doc, "pods/portforward") {
			t.Errorf("%s: pods/portforward rendered with an external endpoint published:\n%s", clientConnectTpl, doc)
		}
		if !strings.Contains(doc, `resources: ["connections"]`) {
			t.Errorf("%s: connections rule missing:\n%s", clientConnectTpl, doc)
		}
	})

	t.Run("gate=portforward", func(t *testing.T) {
		out := renderChart(t, vals("portforward"))
		if !rendered(out, clientConnectTpl) {
			t.Fatalf("%s did not render", clientConnectTpl)
		}
		doc := out[clientConnectTpl]
		if !strings.Contains(doc, "resourceNames:\n      - traffic-manager-0") {
			t.Errorf("%s: named pods/portforward grant missing; the portforward gate reviews possession of it:\n%s", clientConnectTpl, doc)
		}
		if strings.Contains(doc, `resources: ["connections"]`) {
			t.Errorf("%s: connections rule rendered under gate=portforward:\n%s", clientConnectTpl, doc)
		}
		for _, absent := range []string{`resources: ["pods"]`, `resources: ["services"]`} {
			if strings.Contains(doc, absent) {
				t.Errorf("%s: discovery rule %q rendered with an external endpoint published:\n%s", clientConnectTpl, absent, doc)
			}
		}
	})
}

// TestExternalEndpointRequiresEnforcing asserts that externalEndpoint.enabled
// under any auth mode other than enforcing fails the render -- the plan
// mandates a hard failure since permissive/disabled would leave the public
// listener with no access control.
func TestExternalEndpointRequiresEnforcing(t *testing.T) {
	for _, mode := range []string{"permissive", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			err := renderErr(t, map[string]any{
				"security": map[string]any{
					"authentication": map[string]any{"mode": mode},
				},
				"externalEndpoint": map[string]any{
					"enabled": true,
					"tls":     map[string]any{"secretName": "rtest-external-tls"},
				},
			})
			if err == nil {
				t.Fatalf("expected render to fail under security.authentication.mode=%s", mode)
			}
			if !strings.Contains(err.Error(), "enforcing") {
				t.Errorf("unexpected render error: %v", err)
			}
		})
	}
}

// TestExternalEndpointRequiresExactlyOneTLSSource asserts that
// externalEndpoint.enabled fails the render unless exactly one of
// tls.secretName / tls.certManager.enabled is set: neither leaves the
// listener without a certificate, and both is an ambiguous configuration.
func TestExternalEndpointRequiresExactlyOneTLSSource(t *testing.T) {
	base := map[string]any{
		"security": map[string]any{
			"authentication": map[string]any{"mode": "enforcing"},
		},
	}

	t.Run("neither", func(t *testing.T) {
		vals := map[string]any{
			"security":         base["security"],
			"externalEndpoint": map[string]any{"enabled": true},
		}
		err := renderErr(t, vals)
		if err == nil {
			t.Fatalf("expected render to fail with neither tls.secretName nor tls.certManager.enabled set")
		}
		if !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("unexpected render error: %v", err)
		}
	})

	t.Run("both", func(t *testing.T) {
		vals := map[string]any{
			"security": base["security"],
			"externalEndpoint": map[string]any{
				"enabled": true,
				"tls": map[string]any{
					"secretName":  "rtest-external-tls",
					"certManager": map[string]any{"enabled": true},
				},
			},
		}
		err := renderErr(t, vals)
		if err == nil {
			t.Fatalf("expected render to fail with both tls.secretName and tls.certManager.enabled set")
		}
		if !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("unexpected render error: %v", err)
		}
	})
}

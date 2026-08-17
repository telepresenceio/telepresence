package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func externalValuesEnabled(mod ...func(map[string]any)) map[string]any {
	v := map[string]any{"externalEndpoint": map[string]any{"enabled": true}}
	for _, m := range mod {
		m(v)
	}
	return v
}

func externalService(svcType corev1.ServiceType, mod ...func(*corev1.Service)) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: externalServiceName, Namespace: "ambassador"},
		Spec:       corev1.ServiceSpec{Type: svcType},
	}
	for _, m := range mod {
		m(svc)
	}
	return svc
}

// fakeExternalProbe returns a canned result regardless of its arguments.
func fakeExternalProbe(r externalProbeResult) externalProber {
	return func(context.Context, string, []byte, string) externalProbeResult { return r }
}

// unusedExternalProbe fails the test if the probe is invoked at all: used to
// prove that address resolution stopped before reaching it.
func unusedExternalProbe(t *testing.T) externalProber {
	t.Helper()
	return func(context.Context, string, []byte, string) externalProbeResult {
		t.Fatal("external probe should not have been invoked")
		return externalProbeResult{}
	}
}

// -- dial address resolution --

func TestExternalDialAddr_LoadBalancer(t *testing.T) {
	t.Run("IP ingress", func(t *testing.T) {
		svc := externalService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Name: externalPortName, Port: 443}}
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
		})
		addr, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3.4:443", addr)
	})
	t.Run("hostname ingress", func(t *testing.T) {
		svc := externalService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Name: externalPortName, Port: 443}}
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "tm.example.com"}}
		})
		addr, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "tm.example.com:443", addr)
	})
	t.Run("sole unnamed port", func(t *testing.T) {
		svc := externalService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Port: 443}}
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
		})
		addr, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3.4:443", addr)
	})
	t.Run("no ingress assigned", func(t *testing.T) {
		svc := externalService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Name: externalPortName, Port: 443}}
		})
		_, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
		assert.Error(t, err)
	})
}

func TestExternalDialAddr_NodePort(t *testing.T) {
	svc := externalService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
		svc.Spec.Ports = []corev1.ServicePort{{Name: externalPortName, Port: 443, NodePort: 31443}}
	})
	t.Run("node address available", func(t *testing.T) {
		client := fake.NewClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "172.18.0.2"},
			}},
		})
		addr, err := externalDialAddr(context.Background(), client, svc)
		require.NoError(t, err)
		assert.Equal(t, "172.18.0.2:31443", addr)
	})
	t.Run("no node has a usable address", func(t *testing.T) {
		client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}})
		_, err := externalDialAddr(context.Background(), client, svc)
		assert.Error(t, err)
	})
	t.Run("no allocated node port", func(t *testing.T) {
		unallocated := externalService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Name: externalPortName, Port: 443}}
		})
		_, err := externalDialAddr(context.Background(), fake.NewClientset(), unallocated)
		assert.Error(t, err)
	})
}

func TestExternalDialAddr_ClusterIPUnsupported(t *testing.T) {
	svc := externalService(corev1.ServiceTypeClusterIP)
	_, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
	assert.Error(t, err)
}

// -- verifyExternalEndpoint: address resolution gating the probe --

func TestVerifyExternalEndpoint_ClusterIPSkipsProbe(t *testing.T) {
	client := fake.NewClientset(externalService(corev1.ServiceTypeClusterIP))
	notes := verifyExternalEndpoint(context.Background(), client, "ambassador", externalValuesEnabled(), ClientAuthFacts{}, unusedExternalProbe(t))
	require.Len(t, notes, 1)
	assert.Equal(t, NoteInfo, notes[0].Level)
	assert.Contains(t, notes[0].Text, "ClusterIP")
	assert.Contains(t, notes[0].Text, "not reachable from outside the cluster")
}

func TestVerifyExternalEndpoint_ServiceMissing(t *testing.T) {
	client := fake.NewClientset()
	notes := verifyExternalEndpoint(context.Background(), client, "ambassador", externalValuesEnabled(), ClientAuthFacts{}, unusedExternalProbe(t))
	require.Len(t, notes, 1)
	assert.Equal(t, NoteWarning, notes[0].Level)
	assert.Contains(t, notes[0].Text, "was not found")
}

func TestVerifyExternalEndpoint_LoadBalancerNoIngressWithinDeadline(t *testing.T) {
	client := fake.NewClientset(externalService(corev1.ServiceTypeLoadBalancer))
	notes := verifyExternalEndpoint(shortCtx(t), client, "ambassador", externalValuesEnabled(), ClientAuthFacts{}, unusedExternalProbe(t))
	require.Len(t, notes, 1)
	assert.Equal(t, NoteWarning, notes[0].Level)
	assert.Contains(t, notes[0].Text, "no external endpoint ingress yet")
}

func TestVerifyExternalEndpoint_NodePortNoNodeAddress(t *testing.T) {
	client := fake.NewClientset(externalService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
		svc.Spec.Ports = []corev1.ServicePort{{Name: externalPortName, Port: 443, NodePort: 31443}}
	}))
	notes := verifyExternalEndpoint(context.Background(), client, "ambassador", externalValuesEnabled(), ClientAuthFacts{}, unusedExternalProbe(t))
	require.Len(t, notes, 1)
	assert.Equal(t, NoteWarning, notes[0].Level)
	assert.Contains(t, notes[0].Text, "dial address could not be determined")
}

// -- verifyExternalEndpoint: report rendering of a successful and failed probe --

func nodePortReadyService() *corev1.Service {
	return externalService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
		svc.Spec.Ports = []corev1.ServicePort{{Name: externalPortName, Port: 443, NodePort: 31443}}
	})
}

func nodePortReadyNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1"},
		Status:     corev1.NodeStatus{Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.18.0.2"}}},
	}
}

// externalTLSSecret is the Secret externalValuesWithTLS names, so CA
// resolution succeeds without adding its own warning note.
func externalTLSSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "tm-tls", Namespace: "ambassador"},
		Data:       map[string][]byte{"ca.crt": []byte("CA-PEM")},
	}
}

func externalValuesWithTLS() map[string]any {
	return externalValuesEnabled(func(v map[string]any) {
		v["externalEndpoint"].(map[string]any)["tls"] = map[string]any{"secretName": "tm-tls"}
	})
}

func TestVerifyExternalEndpoint_FullSuccess(t *testing.T) {
	client := fake.NewClientset(nodePortReadyService(), nodePortReadyNode(), externalTLSSecret())
	result := externalProbeResult{
		TLS:     Finding{Verdict: VerdictYes, Evidence: []string{"TLS handshake to 172.18.0.2:31443 succeeded"}},
		Version: Finding{Verdict: VerdictYes, Evidence: []string{"Version call succeeded (traffic-manager v2.99.0)"}},
		Auth:    Finding{Verdict: VerdictYes, Evidence: []string{"authenticated-session probe succeeded (ArriveAsClient + Depart)"}},
	}
	ctx := fakeRestConfigCtx{context.Background(), &rest.Config{BearerToken: "tok"}}
	notes := verifyExternalEndpoint(ctx, client, "ambassador", externalValuesWithTLS(), ClientAuthFacts{Bearer: true}, fakeExternalProbe(result))

	require.Len(t, notes, 4)
	assert.Equal(t, NoteInfo, notes[0].Level)
	assert.Contains(t, notes[0].Text, "TLS handshake")
	assert.Equal(t, NoteInfo, notes[1].Level)
	assert.Contains(t, notes[1].Text, "Version call succeeded")

	// the ready-to-paste client config block
	assert.Equal(t, NoteInfo, notes[2].Level)
	assert.Contains(t, notes[2].Text, "cluster:")
	assert.Contains(t, notes[2].Text, "managerAddress: tls://172.18.0.2:31443")
	assert.Contains(t, notes[2].Text, "managerServerCA:")

	assert.Equal(t, NoteInfo, notes[3].Level)
	assert.Contains(t, notes[3].Text, "authenticated-session probe succeeded")
}

func TestVerifyExternalEndpoint_TLSFailureStopsAtThatFinding(t *testing.T) {
	client := fake.NewClientset(nodePortReadyService(), nodePortReadyNode(), externalTLSSecret())
	result := externalProbeResult{
		TLS:     Finding{Verdict: VerdictNo, Evidence: []string{"TLS handshake to 172.18.0.2:31443 failed: x509: certificate signed by unknown authority"}},
		Version: Finding{Verdict: VerdictUnknown, Evidence: []string{"skipped: the TLS handshake did not succeed"}},
		Auth:    Finding{Verdict: VerdictUnknown, Evidence: []string{"skipped: the TLS handshake did not succeed"}},
	}
	ctx := fakeRestConfigCtx{context.Background(), &rest.Config{BearerToken: "tok"}}
	notes := verifyExternalEndpoint(ctx, client, "ambassador", externalValuesWithTLS(), ClientAuthFacts{Bearer: true}, fakeExternalProbe(result))

	require.Len(t, notes, 3)
	assert.Equal(t, NoteWarning, notes[0].Level)
	assert.Contains(t, notes[0].Text, "TLS handshake")
	assert.Contains(t, notes[0].Text, "failed")
	assert.Equal(t, NoteWarning, notes[1].Level)
	assert.Contains(t, notes[1].Text, "skipped")
	assert.Equal(t, NoteWarning, notes[2].Level)
	assert.Contains(t, notes[2].Text, "skipped")
	// no client config block on a failed probe
	for _, n := range notes {
		assert.NotContains(t, n.Text, "managerAddress")
	}
}

func TestVerifyExternalEndpoint_AuthSkippedWithoutBearerCredential(t *testing.T) {
	client := fake.NewClientset(nodePortReadyService(), nodePortReadyNode(), externalTLSSecret())
	result := externalProbeResult{
		TLS:     Finding{Verdict: VerdictYes, Evidence: []string{"TLS handshake to 172.18.0.2:31443 succeeded"}},
		Version: Finding{Verdict: VerdictYes, Evidence: []string{"Version call succeeded (traffic-manager v2.99.0)"}},
		Auth:    Finding{Verdict: VerdictUnknown, Evidence: []string{"skipped: no bearer token available for this probe"}},
	}
	// ClientAuth.Bearer is false: this kubeconfig is cert-only, so the probe never even tried to fetch a token.
	notes := verifyExternalEndpoint(context.Background(), client, "ambassador", externalValuesWithTLS(), ClientAuthFacts{X509: true}, fakeExternalProbe(result))

	require.Len(t, notes, 4)
	assert.Equal(t, NoteWarning, notes[3].Level)
	assert.Contains(t, notes[3].Text, "skipped")
}

func TestVerifyExternalEndpoint_MissingCAAddsWarningButStillProbes(t *testing.T) {
	client := fake.NewClientset(nodePortReadyService(), nodePortReadyNode())
	result := externalProbeResult{
		TLS:     Finding{Verdict: VerdictYes, Evidence: []string{"TLS handshake to 172.18.0.2:31443 succeeded"}},
		Version: Finding{Verdict: VerdictYes, Evidence: []string{"Version call succeeded (traffic-manager v2.99.0)"}},
		Auth:    Finding{Verdict: VerdictUnknown, Evidence: []string{"skipped: no bearer token available for this probe"}},
	}
	// externalValuesEnabled configures no tls.secretName/certManager, so the CA cannot be resolved.
	notes := verifyExternalEndpoint(context.Background(), client, "ambassador", externalValuesEnabled(), ClientAuthFacts{}, fakeExternalProbe(result))

	require.Len(t, notes, 5)
	assert.Equal(t, NoteWarning, notes[0].Level)
	assert.Contains(t, notes[0].Text, "could not read the external listener's CA")
}

// -- CA resolution --

func TestExternalTLSSecretName(t *testing.T) {
	t.Run("explicit secretName", func(t *testing.T) {
		v := externalValuesEnabled(func(v map[string]any) {
			v["externalEndpoint"].(map[string]any)["tls"] = map[string]any{"secretName": "my-tls-secret"}
		})
		assert.Equal(t, "my-tls-secret", externalTLSSecretName(v))
	})
	t.Run("certManager enabled", func(t *testing.T) {
		v := externalValuesEnabled(func(v map[string]any) {
			v["externalEndpoint"].(map[string]any)["tls"] = map[string]any{"certManager": map[string]any{"enabled": true}}
		})
		assert.Equal(t, certManagerSecretName, externalTLSSecretName(v))
	})
	t.Run("neither configured", func(t *testing.T) {
		assert.Empty(t, externalTLSSecretName(externalValuesEnabled()))
	})
}

func TestExternalCA(t *testing.T) {
	values := externalValuesEnabled(func(v map[string]any) {
		v["externalEndpoint"].(map[string]any)["tls"] = map[string]any{"secretName": "tm-tls"}
	})
	t.Run("prefers ca.crt", func(t *testing.T) {
		client := fake.NewClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "tm-tls", Namespace: "ambassador"},
			Data:       map[string][]byte{"ca.crt": []byte("CA-PEM"), "tls.crt": []byte("LEAF-PEM")},
		})
		ca, err := externalCA(context.Background(), client, "ambassador", values)
		require.NoError(t, err)
		assert.Equal(t, []byte("CA-PEM"), ca)
	})
	t.Run("falls back to tls.crt", func(t *testing.T) {
		client := fake.NewClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "tm-tls", Namespace: "ambassador"},
			Data:       map[string][]byte{"tls.crt": []byte("LEAF-PEM")},
		})
		ca, err := externalCA(context.Background(), client, "ambassador", values)
		require.NoError(t, err)
		assert.Equal(t, []byte("LEAF-PEM"), ca)
	})
	t.Run("secret missing", func(t *testing.T) {
		_, err := externalCA(context.Background(), fake.NewClientset(), "ambassador", values)
		assert.Error(t, err)
	})
	t.Run("secret has neither key", func(t *testing.T) {
		client := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tm-tls", Namespace: "ambassador"}})
		_, err := externalCA(context.Background(), client, "ambassador", values)
		assert.Error(t, err)
	})
	t.Run("no secret named in values", func(t *testing.T) {
		_, err := externalCA(context.Background(), fake.NewClientset(), "ambassador", externalValuesEnabled())
		assert.Error(t, err)
	})
}

// -- bearer token resolution --

func TestExternalBearerToken(t *testing.T) {
	t.Run("static token", func(t *testing.T) {
		tok, err := externalBearerToken(context.Background(), &rest.Config{BearerToken: "abc123"})
		require.NoError(t, err)
		assert.Equal(t, "abc123", tok)
	})
	t.Run("token file", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "token")
		require.NoError(t, os.WriteFile(f, []byte("file-token\n"), 0o600))
		tok, err := externalBearerToken(context.Background(), &rest.Config{BearerTokenFile: f})
		require.NoError(t, err)
		assert.Equal(t, "file-token", tok)
	})
	t.Run("nil rest config", func(t *testing.T) {
		_, err := externalBearerToken(context.Background(), nil)
		assert.Error(t, err)
	})
	t.Run("no credential source", func(t *testing.T) {
		_, err := externalBearerToken(context.Background(), &rest.Config{})
		assert.Error(t, err)
	})
}

// -- ambient REST config extraction from ctx --

type fakeRestConfigCtx struct {
	context.Context
	rc *rest.Config
}

func (f fakeRestConfigCtx) GetRestConfig() *rest.Config { return f.rc }

func TestRestConfigFrom(t *testing.T) {
	t.Run("plain context yields nil", func(t *testing.T) {
		assert.Nil(t, restConfigFrom(context.Background()))
	})
	t.Run("a context implementing the provider yields its config", func(t *testing.T) {
		rc := &rest.Config{BearerToken: "tok"}
		assert.Same(t, rc, restConfigFrom(fakeRestConfigCtx{context.Background(), rc}))
	})
}

// -- client config note --

func TestClientConfigNote(t *testing.T) {
	n := clientConfigNote("1.2.3.4:443", []byte("-----BEGIN CERTIFICATE-----\nABC\n-----END CERTIFICATE-----\n"))
	assert.Equal(t, NoteInfo, n.Level)
	assert.Contains(t, n.Text, "managerAddress: tls://1.2.3.4:443")
	assert.Contains(t, n.Text, "managerServerCA:")
	assert.NotContains(t, n.Text, "BEGIN CERTIFICATE") // it's base64-encoded, not the raw PEM
}

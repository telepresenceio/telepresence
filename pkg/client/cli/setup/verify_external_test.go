package setup

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

func externalValuesEnabled(mod ...func(*helm.Values)) *helm.Values {
	v := &helm.Values{ExternalEndpoint: helm.ExternalEndpoint{Enabled: new(true)}}
	for _, m := range mod {
		m(v)
	}
	return v
}

func externalService(svcType core.ServiceType, mod ...func(*core.Service)) *core.Service {
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: externalServiceName, Namespace: "ambassador"},
		Spec:       core.ServiceSpec{Type: svcType},
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
		svc := externalService(core.ServiceTypeLoadBalancer, func(svc *core.Service) {
			svc.Spec.Ports = []core.ServicePort{{Name: externalPortName, Port: 443}}
			svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{IP: "1.2.3.4"}}
		})
		addr, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3.4:443", addr)
	})
	t.Run("hostname ingress", func(t *testing.T) {
		svc := externalService(core.ServiceTypeLoadBalancer, func(svc *core.Service) {
			svc.Spec.Ports = []core.ServicePort{{Name: externalPortName, Port: 443}}
			svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{Hostname: "tm.example.com"}}
		})
		addr, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "tm.example.com:443", addr)
	})
	t.Run("sole unnamed port", func(t *testing.T) {
		svc := externalService(core.ServiceTypeLoadBalancer, func(svc *core.Service) {
			svc.Spec.Ports = []core.ServicePort{{Port: 443}}
			svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{IP: "1.2.3.4"}}
		})
		addr, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3.4:443", addr)
	})
	t.Run("no ingress assigned", func(t *testing.T) {
		svc := externalService(core.ServiceTypeLoadBalancer, func(svc *core.Service) {
			svc.Spec.Ports = []core.ServicePort{{Name: externalPortName, Port: 443}}
		})
		_, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
		assert.Error(t, err)
	})
}

func TestExternalDialAddr_NodePort(t *testing.T) {
	svc := externalService(core.ServiceTypeNodePort, func(svc *core.Service) {
		svc.Spec.Ports = []core.ServicePort{{Name: externalPortName, Port: 443, NodePort: 31443}}
	})
	t.Run("node address available", func(t *testing.T) {
		client := fake.NewClientset(&core.Node{
			ObjectMeta: meta.ObjectMeta{Name: "n1"},
			Status: core.NodeStatus{Addresses: []core.NodeAddress{
				{Type: core.NodeInternalIP, Address: "172.18.0.2"},
			}},
		})
		addr, err := externalDialAddr(context.Background(), client, svc)
		require.NoError(t, err)
		assert.Equal(t, "172.18.0.2:31443", addr)
	})
	t.Run("no node has a usable address", func(t *testing.T) {
		client := fake.NewClientset(&core.Node{ObjectMeta: meta.ObjectMeta{Name: "n1"}})
		_, err := externalDialAddr(context.Background(), client, svc)
		assert.Error(t, err)
	})
	t.Run("no allocated node port", func(t *testing.T) {
		unallocated := externalService(core.ServiceTypeNodePort, func(svc *core.Service) {
			svc.Spec.Ports = []core.ServicePort{{Name: externalPortName, Port: 443}}
		})
		_, err := externalDialAddr(context.Background(), fake.NewClientset(), unallocated)
		assert.Error(t, err)
	})
}

func TestExternalDialAddr_ClusterIPUnsupported(t *testing.T) {
	svc := externalService(core.ServiceTypeClusterIP)
	_, err := externalDialAddr(context.Background(), fake.NewClientset(), svc)
	assert.Error(t, err)
}

// -- verifyExternalEndpoint: address resolution gating the probe --

func TestVerifyExternalEndpoint_ClusterIPSkipsProbe(t *testing.T) {
	client := fake.NewClientset(externalService(core.ServiceTypeClusterIP))
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
	client := fake.NewClientset(externalService(core.ServiceTypeLoadBalancer))
	notes := verifyExternalEndpoint(shortCtx(t), client, "ambassador", externalValuesEnabled(), ClientAuthFacts{}, unusedExternalProbe(t))
	require.Len(t, notes, 1)
	assert.Equal(t, NoteWarning, notes[0].Level)
	assert.Contains(t, notes[0].Text, "no external endpoint ingress yet")
}

func TestVerifyExternalEndpoint_NodePortNoNodeAddress(t *testing.T) {
	client := fake.NewClientset(externalService(core.ServiceTypeNodePort, func(svc *core.Service) {
		svc.Spec.Ports = []core.ServicePort{{Name: externalPortName, Port: 443, NodePort: 31443}}
	}))
	notes := verifyExternalEndpoint(context.Background(), client, "ambassador", externalValuesEnabled(), ClientAuthFacts{}, unusedExternalProbe(t))
	require.Len(t, notes, 1)
	assert.Equal(t, NoteWarning, notes[0].Level)
	assert.Contains(t, notes[0].Text, "dial address could not be determined")
}

// -- verifyExternalEndpoint: report rendering of a successful and failed probe --

func nodePortReadyService() *core.Service {
	return externalService(core.ServiceTypeNodePort, func(svc *core.Service) {
		svc.Spec.Ports = []core.ServicePort{{Name: externalPortName, Port: 443, NodePort: 31443}}
	})
}

func nodePortReadyNode() *core.Node {
	return &core.Node{
		ObjectMeta: meta.ObjectMeta{Name: "n1"},
		Status:     core.NodeStatus{Addresses: []core.NodeAddress{{Type: core.NodeInternalIP, Address: "172.18.0.2"}}},
	}
}

// externalTLSSecret is the Secret externalValuesWithTLS names, so CA
// resolution succeeds without adding its own warning note.
func externalTLSSecret() *core.Secret {
	return &core.Secret{
		ObjectMeta: meta.ObjectMeta{Name: "tm-tls", Namespace: "ambassador"},
		Data:       map[string][]byte{"ca.crt": []byte("CA-PEM")},
	}
}

func externalValuesWithTLS() *helm.Values {
	return externalValuesEnabled(func(v *helm.Values) {
		v.ExternalEndpoint.TLS = helm.ExternalTLS{SecretName: new("tm-tls")}
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
		v := externalValuesEnabled(func(v *helm.Values) {
			v.ExternalEndpoint.TLS = helm.ExternalTLS{SecretName: new("my-tls-secret")}
		})
		assert.Equal(t, "my-tls-secret", externalTLSSecretName(v))
	})
	t.Run("certManager enabled", func(t *testing.T) {
		v := externalValuesEnabled(func(v *helm.Values) {
			v.ExternalEndpoint.TLS = helm.ExternalTLS{CertManager: helm.CertManager{Enabled: new(true)}}
		})
		assert.Equal(t, certManagerSecretName, externalTLSSecretName(v))
	})
	t.Run("neither configured", func(t *testing.T) {
		assert.Empty(t, externalTLSSecretName(externalValuesEnabled()))
	})
}

func TestExternalCA(t *testing.T) {
	values := externalValuesEnabled(func(v *helm.Values) {
		v.ExternalEndpoint.TLS = helm.ExternalTLS{SecretName: new("tm-tls")}
	})
	t.Run("prefers ca.crt", func(t *testing.T) {
		client := fake.NewClientset(&core.Secret{
			ObjectMeta: meta.ObjectMeta{Name: "tm-tls", Namespace: "ambassador"},
			Data:       map[string][]byte{"ca.crt": []byte("CA-PEM"), "tls.crt": []byte("LEAF-PEM")},
		})
		ca, err := externalCA(context.Background(), client, "ambassador", values)
		require.NoError(t, err)
		assert.Equal(t, []byte("CA-PEM"), ca)
	})
	t.Run("falls back to tls.crt", func(t *testing.T) {
		client := fake.NewClientset(&core.Secret{
			ObjectMeta: meta.ObjectMeta{Name: "tm-tls", Namespace: "ambassador"},
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
		client := fake.NewClientset(&core.Secret{ObjectMeta: meta.ObjectMeta{Name: "tm-tls", Namespace: "ambassador"}})
		_, err := externalCA(context.Background(), client, "ambassador", values)
		assert.Error(t, err)
	})
	t.Run("no secret named in values", func(t *testing.T) {
		_, err := externalCA(context.Background(), fake.NewClientset(), "ambassador", externalValuesEnabled())
		assert.Error(t, err)
	})
}

// certManagerValues selects the cert-manager path, the one for which
// externalCA waits for the Secret to appear.
func certManagerValues() *helm.Values {
	return externalValuesEnabled(func(v *helm.Values) {
		v.ExternalEndpoint.TLS = helm.ExternalTLS{CertManager: helm.CertManager{Enabled: new(true)}}
	})
}

func TestExternalCA_CertManagerWait(t *testing.T) {
	t.Run("secret appears after a short wait", func(t *testing.T) {
		client := fake.NewClientset()
		secret := &core.Secret{
			ObjectMeta: meta.ObjectMeta{Name: certManagerSecretName, Namespace: "ambassador"},
			Data:       map[string][]byte{"tls.crt": []byte("LEAF-PEM")},
		}
		var calls int32
		client.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			// The first lookup finds nothing, proving the wait loop must
			// poll at least once; the tracker gains the Secret shortly
			// after, before the loop's next tick.
			if atomic.AddInt32(&calls, 1) == 1 {
				return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, certManagerSecretName)
			}
			return false, nil, nil
		})
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = client.Tracker().Add(secret)
		}()

		ca, err := externalCA(context.Background(), client, "ambassador", certManagerValues())
		require.NoError(t, err)
		assert.Equal(t, []byte("LEAF-PEM"), ca)
		assert.GreaterOrEqual(t, atomic.LoadInt32(&calls), int32(2))
	})

	t.Run("never issued times out with a clear error", func(t *testing.T) {
		_, err := externalCA(shortCtx(t), fake.NewClientset(), "ambassador", certManagerValues())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cert-manager certificate was not issued within the verification window")
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

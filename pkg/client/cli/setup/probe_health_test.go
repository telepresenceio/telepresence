package setup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func managerDeployment(desired, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: managerDeploymentName, Namespace: "ambassador"},
		Spec:       appsv1.DeploymentSpec{Replicas: &desired},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func installedRelease(values map[string]any) *ReleaseFacts {
	return &ReleaseFacts{
		Installed: true,
		Version:   version.Structured.String(),
		Namespace: "ambassador",
		Values:    values,
	}
}

// pemCert returns a self-signed certificate expiring at notAfter, PEM-encoded
// the way the chart stores it in the webhook's CA bundle.
func pemCert(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "agent-injector-ca"},
		NotBefore:    notAfter.Add(-24 * 365 * time.Hour),
		NotAfter:     notAfter,
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func webhookConfiguration(caBundle []byte) *admissionv1.MutatingWebhookConfiguration {
	return &admissionv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: webhookConfigurationPrefix + "ambassador"},
		Webhooks: []admissionv1.MutatingWebhook{
			{
				Name:         "agent-injector-ambassador.telepresence.io",
				ClientConfig: admissionv1.WebhookClientConfig{CABundle: caBundle},
			},
		},
	}
}

func TestProbeHealth_ManagerReadiness(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(managerDeployment(1, 1)), ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(nil), ClientAuthFacts{})
		assert.Equal(t, VerdictYes, h.ManagerReady.Verdict)
		assert.Contains(t, h.ManagerReady.Evidence[0], "1 of 1 replicas ready")
	})
	t.Run("unready with warning events as evidence", func(t *testing.T) {
		client := fake.NewClientset(
			managerDeployment(1, 0),
			&eventsv1.Event{
				ObjectMeta: metav1.ObjectMeta{Name: "ev1", Namespace: "ambassador"},
				Type:       "Warning",
				Reason:     "BackOff",
				Note:       "Back-off pulling image",
				Regarding:  corev1.ObjectReference{Kind: "Pod", Name: "traffic-manager-abc123-xyz", Namespace: "ambassador"},
			},
			&eventsv1.Event{
				ObjectMeta: metav1.ObjectMeta{Name: "ev2", Namespace: "ambassador"},
				Type:       "Normal",
				Reason:     "Pulled",
				Regarding:  corev1.ObjectReference{Kind: "Pod", Name: "traffic-manager-abc123-xyz", Namespace: "ambassador"},
			},
			&eventsv1.Event{
				ObjectMeta: metav1.ObjectMeta{Name: "ev3", Namespace: "ambassador"},
				Type:       "Warning",
				Reason:     "Unrelated",
				Regarding:  corev1.ObjectReference{Name: "other-pod", Namespace: "ambassador"},
			},
		)
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(nil), ClientAuthFacts{})
		require.Equal(t, VerdictNo, h.ManagerReady.Verdict)
		assert.Contains(t, h.ManagerReady.Evidence[0], "0 of 1 replicas ready")
		require.Len(t, h.ManagerReady.Evidence, 2)
		assert.Equal(t, "BackOff: Back-off pulling image", h.ManagerReady.Evidence[1])
	})
	t.Run("scaled to zero", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(managerDeployment(0, 0)), ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(nil), ClientAuthFacts{})
		require.Equal(t, VerdictNo, h.ManagerReady.Verdict)
		assert.Contains(t, h.ManagerReady.Evidence[0], "scaled to zero")
	})
	t.Run("deployment missing", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(), ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(nil), ClientAuthFacts{})
		assert.Equal(t, VerdictNo, h.ManagerReady.Verdict)
	})
	t.Run("denial is unknown", func(t *testing.T) {
		client := fake.NewClientset()
		client.PrependReactor("get", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, managerDeploymentName, nil)
		})
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(nil), ClientAuthFacts{})
		assert.Equal(t, VerdictUnknown, h.ManagerReady.Verdict)
	})
}

func TestProbeHealth_Webhook(t *testing.T) {
	injectorValues := map[string]any{"agentInjector": map[string]any{"enabled": true}}

	t.Run("present with a healthy certificate", func(t *testing.T) {
		client := fake.NewClientset(managerDeployment(1, 1), webhookConfiguration(pemCert(t, time.Now().Add(300*24*time.Hour))))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(injectorValues), ClientAuthFacts{})
		require.NotNil(t, h.Webhook)
		assert.Equal(t, VerdictYes, h.Webhook.Verdict)
		require.NotNil(t, h.Certificate)
		assert.Equal(t, VerdictYes, h.Certificate.Verdict)
		assert.Contains(t, h.Certificate.Evidence[0], "valid until")
		require.NotNil(t, h.InjectorEndpoints)
		assert.Equal(t, VerdictNo, h.InjectorEndpoints.Verdict)
	})
	t.Run("expired certificate", func(t *testing.T) {
		client := fake.NewClientset(managerDeployment(1, 1), webhookConfiguration(pemCert(t, time.Now().Add(-time.Hour))))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(injectorValues), ClientAuthFacts{})
		require.NotNil(t, h.Certificate)
		assert.Equal(t, VerdictNo, h.Certificate.Verdict)
		assert.Contains(t, h.Certificate.Evidence[0], "expired")
	})
	t.Run("certificate expiring within 30 days", func(t *testing.T) {
		client := fake.NewClientset(managerDeployment(1, 1), webhookConfiguration(pemCert(t, time.Now().Add(10*24*time.Hour))))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(injectorValues), ClientAuthFacts{})
		require.NotNil(t, h.Certificate)
		assert.Equal(t, VerdictNo, h.Certificate.Verdict)
		assert.Contains(t, h.Certificate.Evidence[0], "expires within 30 days")
	})
	t.Run("unparseable bundle", func(t *testing.T) {
		client := fake.NewClientset(managerDeployment(1, 1), webhookConfiguration([]byte("not a pem")))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(injectorValues), ClientAuthFacts{})
		require.NotNil(t, h.Certificate)
		assert.Equal(t, VerdictUnknown, h.Certificate.Verdict)
	})
	t.Run("missing configuration", func(t *testing.T) {
		client := fake.NewClientset(managerDeployment(1, 1))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(injectorValues), ClientAuthFacts{})
		require.NotNil(t, h.Webhook)
		assert.Equal(t, VerdictNo, h.Webhook.Verdict)
		assert.Nil(t, h.Certificate)
	})
	t.Run("injector not enabled by the release skips the checks", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(managerDeployment(1, 1)), ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(nil), ClientAuthFacts{})
		assert.Nil(t, h.Webhook)
		assert.Nil(t, h.Certificate)
		assert.Nil(t, h.InjectorEndpoints)
	})
}

func TestProbeHealth_QuicAndEndpoints(t *testing.T) {
	t.Run("quic node port allocated", func(t *testing.T) {
		client := fake.NewClientset(managerDeployment(1, 1), quicService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Name: "quic", Port: 7778, NodePort: 31234}}
		}))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(map[string]any{"quicTunnel": map[string]any{"enabled": true}}), ClientAuthFacts{})
		require.NotNil(t, h.Quic)
		assert.Equal(t, VerdictYes, h.Quic.Verdict)
	})
	t.Run("quic service missing", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(managerDeployment(1, 1)), ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(map[string]any{"quicTunnel": map[string]any{"enabled": true}}), ClientAuthFacts{})
		require.NotNil(t, h.Quic)
		assert.Equal(t, VerdictNo, h.Quic.Verdict)
	})
	t.Run("ready injector endpoints", func(t *testing.T) {
		client := fake.NewClientset(managerDeployment(1, 1),
			webhookConfiguration(pemCert(t, time.Now().Add(300*24*time.Hour))), injectorSlice(true))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(map[string]any{"agentInjector": map[string]any{"enabled": true}}), ClientAuthFacts{})
		require.NotNil(t, h.InjectorEndpoints)
		assert.Equal(t, VerdictYes, h.InjectorEndpoints.Verdict)
	})
}

func TestProbeHealth_X509ClientAuth(t *testing.T) {
	enforcingX509Disabled := map[string]any{"security": map[string]any{"authentication": map[string]any{
		"mode": "enforcing", "x509": map[string]any{"enabled": false},
	}}}

	t.Run("cert-only client rejected", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(managerDeployment(1, 1)), ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(enforcingX509Disabled), ClientAuthFacts{X509: true})
		require.NotNil(t, h.X509ClientAuth)
		assert.Equal(t, VerdictNo, h.X509ClientAuth.Verdict)
		assert.Contains(t, h.X509ClientAuth.Evidence[0], "this client will be rejected")
	})
	t.Run("bearer-capable client is unaffected", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(managerDeployment(1, 1)), ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(enforcingX509Disabled), ClientAuthFacts{Bearer: true, X509: true})
		require.NotNil(t, h.X509ClientAuth)
		assert.Equal(t, VerdictYes, h.X509ClientAuth.Verdict)
	})
	t.Run("x509 not explicitly disabled", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(managerDeployment(1, 1)), ManagerNamespace: "ambassador"}
		enforcing := map[string]any{"security": map[string]any{"authentication": map[string]any{"mode": "enforcing"}}}
		h := p.probeHealth(context.Background(), installedRelease(enforcing), ClientAuthFacts{X509: true})
		require.NotNil(t, h.X509ClientAuth)
		assert.Equal(t, VerdictYes, h.X509ClientAuth.Verdict)
	})
	t.Run("client with no usable credentials rejected", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(managerDeployment(1, 1)), ManagerNamespace: "ambassador"}
		enforcing := map[string]any{"security": map[string]any{"authentication": map[string]any{"mode": "enforcing"}}}
		h := p.probeHealth(context.Background(), installedRelease(enforcing), ClientAuthFacts{})
		require.NotNil(t, h.X509ClientAuth)
		assert.Equal(t, VerdictNo, h.X509ClientAuth.Verdict)
		assert.Contains(t, h.X509ClientAuth.Evidence[0], "neither a bearer token nor a client certificate")
	})
	t.Run("not in enforcing mode skips the check", func(t *testing.T) {
		p := &Prober{KubeClient: fake.NewClientset(managerDeployment(1, 1)), ManagerNamespace: "ambassador"}
		h := p.probeHealth(context.Background(), installedRelease(nil), ClientAuthFacts{X509: true})
		assert.Nil(t, h.X509ClientAuth)
	})
}

func TestProbeHealth_VersionSkew(t *testing.T) {
	p := &Prober{KubeClient: fake.NewClientset(managerDeployment(1, 1)), ManagerNamespace: "ambassador"}

	t.Run("equal", func(t *testing.T) {
		h := p.probeHealth(context.Background(), installedRelease(nil), ClientAuthFacts{})
		assert.Equal(t, VerdictYes, h.VersionSkew.Verdict)
	})
	t.Run("older release", func(t *testing.T) {
		rel := installedRelease(nil)
		rel.Version = olderVersion()
		h := p.probeHealth(context.Background(), rel, ClientAuthFacts{})
		require.Equal(t, VerdictNo, h.VersionSkew.Verdict)
		assert.Contains(t, h.VersionSkew.Evidence[0], "older than this client")
	})
	t.Run("newer release", func(t *testing.T) {
		rel := installedRelease(nil)
		rel.Version = newerVersion()
		h := p.probeHealth(context.Background(), rel, ClientAuthFacts{})
		require.Equal(t, VerdictNo, h.VersionSkew.Verdict)
		assert.Contains(t, h.VersionSkew.Evidence[0], "upgrade the client instead")
	})
	t.Run("unparseable release version", func(t *testing.T) {
		rel := installedRelease(nil)
		rel.Version = "not-a-version"
		h := p.probeHealth(context.Background(), rel, ClientAuthFacts{})
		assert.Equal(t, VerdictUnknown, h.VersionSkew.Verdict)
	})
}

package setup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// leafCert returns a self-signed leaf certificate, PEM-encoded the way
// kubernetes.io/tls stores it in tls.crt.
func leafCert(t *testing.T, notAfter time.Time, dnsNames ...string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "external-endpoint"},
		NotBefore:    notAfter.Add(-24 * 365 * time.Hour),
		NotAfter:     notAfter,
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func tlsSecret(name string, crt []byte) *core.Secret {
	return &core.Secret{
		ObjectMeta: meta.ObjectMeta{Name: name, Namespace: "ambassador"},
		Type:       core.SecretTypeTLS,
		Data:       map[string][]byte{"tls.crt": crt},
	}
}

func withCertManagerCRD(client *fake.Clientset) {
	fd := client.Discovery().(*discoveryfake.FakeDiscovery)
	fd.Resources = append(fd.Resources, &meta.APIResourceList{
		GroupVersion: certManagerGroupVersion,
		APIResources: []meta.APIResource{{Name: certManagerResource, Namespaced: true, Kind: "Certificate"}},
	})
}

func TestProbeExternal_CertManager(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		client := fake.NewClientset()
		withCertManagerCRD(client)
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		assert.Equal(t, VerdictYes, facts.CertManager.Verdict)
	})
	t.Run("absent", func(t *testing.T) {
		client := fake.NewClientset()
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		assert.Equal(t, VerdictNo, facts.CertManager.Verdict)
	})
	t.Run("group served without the certificates resource", func(t *testing.T) {
		client := fake.NewClientset()
		fd := client.Discovery().(*discoveryfake.FakeDiscovery)
		fd.Resources = append(fd.Resources, &meta.APIResourceList{GroupVersion: certManagerGroupVersion})
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		assert.Equal(t, VerdictNo, facts.CertManager.Verdict)
	})
	t.Run("discovery denied", func(t *testing.T) {
		client := fake.NewClientset()
		client.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "resource"}, "", nil)
		})
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		assert.Equal(t, VerdictUnknown, facts.CertManager.Verdict)
	})
}

func TestProbeExternal_TLSSecrets(t *testing.T) {
	t.Run("parses DNS names and expiry", func(t *testing.T) {
		client := fake.NewClientset(
			tlsSecret("tm-external-tls", leafCert(t, time.Now().Add(300*24*time.Hour), "tm.example.com")),
			&core.Secret{ObjectMeta: meta.ObjectMeta{Name: "opaque", Namespace: "ambassador"}, Type: core.SecretTypeOpaque},
		)
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		require.Len(t, facts.TLSSecrets, 1)
		s := facts.TLSSecrets[0]
		assert.Equal(t, "tm-external-tls", s.Name)
		assert.Equal(t, []string{"tm.example.com"}, s.DNSNames)
		assert.NotEmpty(t, s.NotAfter)
		assert.False(t, s.Expired)
		assert.False(t, s.ExpiringSoon)
	})
	t.Run("flags an expired certificate", func(t *testing.T) {
		client := fake.NewClientset(tlsSecret("tm-external-tls", leafCert(t, time.Now().Add(-time.Hour))))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		require.Len(t, facts.TLSSecrets, 1)
		assert.True(t, facts.TLSSecrets[0].Expired)
		assert.False(t, facts.TLSSecrets[0].ExpiringSoon)
	})
	t.Run("flags a certificate expiring within 30 days", func(t *testing.T) {
		client := fake.NewClientset(tlsSecret("tm-external-tls", leafCert(t, time.Now().Add(10*24*time.Hour))))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		require.Len(t, facts.TLSSecrets, 1)
		assert.False(t, facts.TLSSecrets[0].Expired)
		assert.True(t, facts.TLSSecrets[0].ExpiringSoon)
	})
	t.Run("unparseable leaf sets ReadError", func(t *testing.T) {
		client := fake.NewClientset(tlsSecret("tm-external-tls", []byte("not a pem")))
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		require.Len(t, facts.TLSSecrets, 1)
		assert.NotEmpty(t, facts.TLSSecrets[0].ReadError)
	})
	t.Run("metadata-only secret yields the name alone", func(t *testing.T) {
		client := fake.NewClientset(&core.Secret{
			ObjectMeta: meta.ObjectMeta{Name: "tm-external-tls", Namespace: "ambassador"},
			Type:       core.SecretTypeTLS,
		})
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		require.Len(t, facts.TLSSecrets, 1)
		assert.Empty(t, facts.TLSSecrets[0].DNSNames)
		assert.Empty(t, facts.TLSSecrets[0].ReadError)
	})
	t.Run("denied list", func(t *testing.T) {
		client := fake.NewClientset()
		client.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", nil)
		})
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		assert.True(t, facts.SecretsListDenied)
		assert.Empty(t, facts.TLSSecrets)
	})
	t.Run("other list error", func(t *testing.T) {
		client := fake.NewClientset()
		client.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("transport failure")
		})
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		assert.False(t, facts.SecretsListDenied)
		assert.NotEmpty(t, facts.SecretsListError)
	})
	t.Run("orders the cert-manager secret first, otherwise by name", func(t *testing.T) {
		client := fake.NewClientset(
			tlsSecret("z-secret", leafCert(t, time.Now().Add(300*24*time.Hour))),
			tlsSecret("a-secret", leafCert(t, time.Now().Add(300*24*time.Hour))),
			tlsSecret(certManagerSecretName, leafCert(t, time.Now().Add(300*24*time.Hour))),
		)
		p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
		facts := p.probeExternal(context.Background())
		require.Len(t, facts.TLSSecrets, 3)
		assert.Equal(t, []string{certManagerSecretName, "a-secret", "z-secret"},
			[]string{facts.TLSSecrets[0].Name, facts.TLSSecrets[1].Name, facts.TLSSecrets[2].Name})
	})
}

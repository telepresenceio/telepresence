package kafkaconfig

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
)

func TestOptionsResolveSASLSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kafka", Namespace: "shop"},
		Data:       map[string][]byte{"username": []byte("alice"), "password": []byte("secret")},
	}).Build()

	options, err := Options(t.Context(), reader, "shop", api.KafkaConnectionSpec{
		BootstrapServers: []string{"kafka:9092"},
		SASL: &api.KafkaSASLSpec{
			Mechanism: "SCRAM-SHA-512",
			Username: &api.ValueSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "kafka"}, Key: "username",
			}},
			Password: &api.ValueSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "kafka"}, Key: "password",
			}},
		},
	})
	require.NoError(t, err)
	require.Len(t, options, 2)
}

func TestOptionsRejectMissingSecretKey(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kafka", Namespace: "shop"},
	}).Build()
	_, err := Options(t.Context(), reader, "shop", api.KafkaConnectionSpec{
		BootstrapServers: []string{"kafka:9092"},
		TLS: &api.KafkaTLSSpec{CA: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "kafka"}, Key: "ca.crt",
		}},
	})
	require.ErrorContains(t, err, "has no key")
}

func TestOptionsResolveTLSAndPlainSecrets(t *testing.T) {
	certificate, privateKey := testCertificate(t)
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kafka", Namespace: "shop"},
		Data: map[string][]byte{
			"ca.crt": certificate, "tls.crt": certificate, "tls.key": privateKey,
			"username": []byte("alice"), "password": []byte("secret"),
		},
	}).Build()
	selector := func(key string) *corev1.SecretKeySelector {
		return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "kafka"}, Key: key}
	}

	options, err := Options(t.Context(), reader, "shop", api.KafkaConnectionSpec{
		BootstrapServers: []string{"kafka:9093"},
		TLS: &api.KafkaTLSSpec{
			CA: selector("ca.crt"), Certificate: selector("tls.crt"), PrivateKey: selector("tls.key"), ServerName: "kafka.shop.svc",
		},
		SASL: &api.KafkaSASLSpec{
			Mechanism: "PLAIN",
			Username:  &api.ValueSource{SecretKeyRef: selector("username")},
			Password:  &api.ValueSource{SecretKeyRef: selector("password")},
		},
	})
	require.NoError(t, err)
	require.Len(t, options, 3)
}

func testCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kafka.shop.svc"},
		DNSNames: []string{"kafka.shop.svc"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certificate, privateKey
}

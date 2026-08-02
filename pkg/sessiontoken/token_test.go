package sessiontoken_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
)

func generateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return key
}

func TestMintVerify_RoundTrip(t *testing.T) {
	key := generateKey(t)
	expiry := time.Now().Add(time.Hour)

	token, err := sessiontoken.Mint(key, "session-1234", expiry)
	require.NoError(t, err)

	sessionID, err := sessiontoken.Verify(&key.PublicKey, token, time.Now())
	require.NoError(t, err)
	require.Equal(t, "session-1234", sessionID)
}

func TestVerify_Expired(t *testing.T) {
	key := generateKey(t)
	expiry := time.Now().Add(time.Hour)
	token, err := sessiontoken.Mint(key, "session-1234", expiry)
	require.NoError(t, err)

	_, err = sessiontoken.Verify(&key.PublicKey, token, expiry.Add(time.Second))
	require.Error(t, err)
}

func TestVerify_WrongKey(t *testing.T) {
	key := generateKey(t)
	other := generateKey(t)
	token, err := sessiontoken.Mint(key, "session-1234", time.Now().Add(time.Hour))
	require.NoError(t, err)

	_, err = sessiontoken.Verify(&other.PublicKey, token, time.Now())
	require.Error(t, err)
}

// TestVerify_Malformed covers the ways a token string can fail to parse or verify:
// empty, wrong version, too few/too many dot-separated parts, an undecodable
// signature, and a well-formed token whose sessionID or expiry was tampered with
// after minting (which invalidates the signature, since both are signed).
func TestVerify_Malformed(t *testing.T) {
	key := generateKey(t)
	valid, err := sessiontoken.Mint(key, "session-1234", time.Now().Add(time.Hour))
	require.NoError(t, err)
	parts := strings.Split(valid, ".")
	require.Len(t, parts, 4)

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"wrong version", "v2." + strings.Join(parts[1:], ".")},
		{"missing parts", strings.Join(parts[:3], ".")},
		{"extra parts", valid + ".extra"},
		{"bad base64 signature", parts[0] + "." + parts[1] + "." + parts[2] + ".not-valid-base64!!"},
		{"tampered session id", parts[0] + ".some-other-session." + parts[2] + "." + parts[3]},
		{"tampered expiry", parts[0] + "." + parts[1] + ".9999999999." + parts[3]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := sessiontoken.Verify(&key.PublicKey, tt.token, time.Now())
			require.Error(t, err)
		})
	}
}

// selfSignedCertPEM returns a minimal self-signed certificate for key, PEM-encoded,
// for exercising PublicKeyFromCertPEM without depending on cmd/traffic/.../quictunnel.
func selfSignedCertPEM(t *testing.T, pub *ecdsa.PublicKey, signer *ecdsa.PrivateKey) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sessiontoken-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, signer)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestPublicKeyFromCertPEM(t *testing.T) {
	key := generateKey(t)
	certPEM := selfSignedCertPEM(t, &key.PublicKey, key)

	pub, err := sessiontoken.PublicKeyFromCertPEM(certPEM)
	require.NoError(t, err)
	require.True(t, pub.Equal(&key.PublicKey))
}

func TestPublicKeyFromCertPEM_NoCertificateBlock(t *testing.T) {
	_, err := sessiontoken.PublicKeyFromCertPEM([]byte("not a PEM block"))
	require.Error(t, err)
}

package quictunnel_test

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/quictunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/sessiontoken"
)

func TestCA_MintClientCert(t *testing.T) {
	ca, err := quictunnel.NewCA()
	require.NoError(t, err)

	certPEM, keyPEM, err := ca.MintClientCert("session-1234")
	require.NoError(t, err)

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)

	require.Equal(t, "session-1234", cert.Subject.CommonName)

	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err, "minted client certificate must verify against the CA")
}

func TestCA_MintClientCert_DistinctKeysPerSession(t *testing.T) {
	ca, err := quictunnel.NewCA()
	require.NoError(t, err)

	cert1, _, err := ca.MintClientCert("session-a")
	require.NoError(t, err)
	cert2, _, err := ca.MintClientCert("session-b")
	require.NoError(t, err)
	require.NotEqual(t, cert1, cert2)
}

func TestCA_ServerTLSCert(t *testing.T) {
	ca, err := quictunnel.NewCA()
	require.NoError(t, err)

	serverCert, err := ca.ServerTLSCert()
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(serverCert.Certificate[0])
	require.NoError(t, err)
	require.Equal(t, quictunnel.ServerName, cert.Subject.CommonName)
	require.Contains(t, cert.DNSNames, quictunnel.ServerName)

	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		DNSName:   quictunnel.ServerName,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	require.NoError(t, err, "minted server certificate must verify against the CA")
}

func TestCA_MintServerCert_ArbitrarySNI(t *testing.T) {
	ca, err := quictunnel.NewCA()
	require.NoError(t, err)

	const sni = "some-pod-uid.agent.telepresence"
	serverCert, err := ca.MintServerCert(sni)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(serverCert.Certificate[0])
	require.NoError(t, err)
	require.Equal(t, sni, cert.Subject.CommonName)
	require.Contains(t, cert.DNSNames, sni)

	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		DNSName:   sni,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	require.NoError(t, err, "minted server certificate must verify against the CA")
}

func TestCA_ServerCertToPEM_RoundTrips(t *testing.T) {
	ca, err := quictunnel.NewCA()
	require.NoError(t, err)

	const sni = "some-pod-uid.agent.telepresence"
	serverCert, err := ca.MintServerCert(sni)
	require.NoError(t, err)

	certPEM, keyPEM, err := quictunnel.ServerCertToPEM(serverCert)
	require.NoError(t, err)

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	require.Equal(t, sni, cert.Subject.CommonName)

	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     ca.Pool(),
		DNSName:   sni,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	require.NoError(t, err, "PEM-round-tripped server certificate must still verify against the CA")
}

func TestCA_MintSessionToken(t *testing.T) {
	ca, err := quictunnel.NewCA()
	require.NoError(t, err)

	before := time.Now()
	token, expiry, err := ca.MintSessionToken("session-1234")
	require.NoError(t, err)
	require.True(t, expiry.After(before), "expiry must be in the future")

	pub, err := sessiontoken.PublicKeyFromCertPEM(ca.CertPEM())
	require.NoError(t, err)

	sessionID, err := sessiontoken.Verify(pub, token, time.Now())
	require.NoError(t, err)
	require.Equal(t, "session-1234", sessionID)
}

func TestCA_DifferentInstancesDoNotCrossTrust(t *testing.T) {
	ca1, err := quictunnel.NewCA()
	require.NoError(t, err)
	ca2, err := quictunnel.NewCA()
	require.NoError(t, err)

	certPEM, keyPEM, err := ca1.MintClientCert("session-1")
	require.NoError(t, err)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)

	_, err = cert.Verify(x509.VerifyOptions{
		Roots:     ca2.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.Error(t, err, "a certificate minted by one CA must not verify against another")
}

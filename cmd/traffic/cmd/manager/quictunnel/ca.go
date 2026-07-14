// Package quictunnel implements the manager side of the QUIC tunnel transport: an
// ephemeral CA and session-scoped client certificates that back the mTLS trust
// bootstrap described in docs/reference/quic-transport-architecture.md, and the QUIC listener
// that accepts tunnel streams authenticated by that CA.
package quictunnel

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// ServerName is the DNS name embedded in the traffic-manager's QUIC server
// certificate. Clients verify the server certificate against exactly this name (not
// against system roots), and the manager advertises it to clients as the
// QuicTunnelEndpoint.ServerName so the two stay in sync.
//
// Behind the packet forwarder, this is also the SNI the forwarder routes a
// connection's first packet on: it must equal pkg/quicfwd.ManagerSNI, which is what
// the forwarder actually reads. pkg/quicfwd duplicates the value rather than
// importing it, because that package must not depend on anything under cmd/;
// pkg/quicfwd/sni_test.go asserts the two constants stay equal.
const ServerName = "traffic-manager.telepresence"

// caValidity bounds the lifetime of the ephemeral CA and the server certificate it
// signs. Both are regenerated every time the manager starts and are never persisted,
// so the only requirement is that the validity period outlives any single manager
// process; it is not a rotation or exposure-window control.
const caValidity = 10 * 365 * 24 * time.Hour

// clientCertValidity is how long a session-scoped client certificate minted by
// MintClientCert remains valid.
const clientCertValidity = 24 * time.Hour

// notBeforeSkew backdates generated certificates slightly so that minor clock skew
// between the manager and a client doesn't make a freshly minted certificate appear
// not-yet-valid.
const notBeforeSkew = 5 * time.Minute

// CA is an ephemeral certificate authority, generated once per manager process, that
// signs the manager's own QUIC server certificate and session-scoped client
// certificates. It is held only in memory; a manager restart generates a new CA and
// implicitly revokes every certificate the previous one signed.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
	pool *x509.CertPool
}

// NewCA generates a new ephemeral ECDSA P-256 CA.
func NewCA() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("quictunnel: generate CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "telepresence-traffic-manager-quic"},
		NotBefore:             now.Add(-notBeforeSkew),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("quictunnel: create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("quictunnel: parse CA certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &CA{
		cert: cert,
		key:  key,
		pem:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pool: pool,
	}, nil
}

// CertPEM returns the CA certificate in PEM form. This is what MintClientCert-issued
// certificates and the ServerTLSCert certificate chain to, and what a client is given
// so it can verify the manager's QUIC server certificate.
func (ca *CA) CertPEM() []byte {
	return ca.pem
}

// Pool returns a cert pool containing only the CA certificate, suitable for
// tls.Config.ClientCAs.
func (ca *CA) Pool() *x509.CertPool {
	return ca.pool
}

// ServerTLSCert mints a server certificate for ServerName, signed by ca, for use in the
// traffic-manager's own QUIC listener's tls.Config. It is a thin wrapper around
// MintServerCert for that one, fixed SNI name.
func (ca *CA) ServerTLSCert() (tls.Certificate, error) {
	return ca.MintServerCert(ServerName)
}

// MintServerCert mints a server certificate for sniName, signed by ca, for use in any
// QUIC listener's tls.Config -- the traffic-manager's own (via ServerTLSCert) or, per
// "Agent connections over QUIC" in docs/reference/quic-transport-architecture.md, a traffic-agent's
// (via the GetQuicAgentCert RPC, which mints for quicfwd.AgentSNI(pod UID)).
func (ca *CA) MintServerCert(sniName string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("quictunnel: generate server key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: sniName},
		DNSNames:     []string{sniName},
		NotBefore:    now.Add(-notBeforeSkew),
		NotAfter:     now.Add(caValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("quictunnel: create server certificate: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}, nil
}

// ServerCertToPEM PEM-encodes cert -- a tls.Certificate produced by MintServerCert or
// ServerTLSCert -- for transfer over the wire (e.g. QuicAgentCert.CertPem/KeyPem). Only
// the ECDSA private keys this package generates are supported.
func ServerCertToPEM(cert tls.Certificate) (certPEM, keyPEM []byte, err error) {
	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("quictunnel: unsupported private key type %T", cert.PrivateKey)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("quictunnel: marshal server key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// MintClientCert mints a short-lived client certificate for sessionID, signed by ca.
// The certificate's CommonName is sessionID; the QUIC listener uses that to bind an
// authenticated connection back to a Telepresence session.
func (ca *CA) MintClientCert(sessionID string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("quictunnel: generate client key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: sessionID},
		NotBefore:    now.Add(-notBeforeSkew),
		NotAfter:     now.Add(clientCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, nil, fmt.Errorf("quictunnel: create client certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("quictunnel: marshal client key: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("quictunnel: generate certificate serial: %w", err)
	}
	return serial, nil
}

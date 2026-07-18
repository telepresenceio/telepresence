package quicserver_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	health "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent/quicserver"
	grpcClient "github.com/telepresenceio/telepresence/v2/pkg/grpc/client"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// testAgentCA is a minimal self-signed CA plus leaf-minting helpers, so tests can build a
// real (verified) TLS chain for a quicserver.Material.
type testAgentCA struct {
	cert *x509.Certificate
	key  ed25519.PrivateKey
	pem  []byte
}

func newTestAgentCA(t *testing.T) *testAgentCA {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "agent-resumption-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &testAgentCA{cert: cert, key: priv, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *testAgentCA) mintLeafPEM(t *testing.T, cn string, dnsNames []string, eku x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// notifyingSessionCache wraps a real tls.ClientSessionCache and signals put so a test can
// wait for the server's post-handshake session ticket to actually land before re-dialing,
// instead of sleeping and hoping.
type notifyingSessionCache struct {
	tls.ClientSessionCache
	put chan struct{}
}

func (c *notifyingSessionCache) Put(sessionKey string, cs *tls.ClientSessionState) {
	c.ClientSessionCache.Put(sessionKey, cs)
	select {
	case c.put <- struct{}{}:
	default:
	}
}

// startTestListener starts a quicserver.Listener presenting material, serving a
// grpc.Server with the standard health service registered (so a test can make one real
// RPC to prove a resumed connection's streams actually work, not just that the TLS
// handshake resumed), and returns the address to dial it on.
func startTestListener(t *testing.T, ctx context.Context, material quicserver.Material) string {
	t.Helper()
	grpcServer := grpc.NewServer()
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, healthSrv)

	ln, err := quicserver.New(netip.MustParseAddr("10.42.3.9"), 0, grpcServer, material)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = ln.Serve(ctx) }()

	udpAddr := ln.Addr().(*net.UDPAddr)
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(udpAddr.Port))
}

// dialAgent dials dialAddr as a QUIC client authenticated for sni, sharing cache across
// calls so a test can observe resumption.
func dialAgent(t *testing.T, ctx context.Context, ca *testAgentCA, dialAddr, sni string, cache tls.ClientSessionCache) *quic.Conn {
	t.Helper()
	clientCertPEM, clientKeyPEM := ca.mintLeafPEM(t, "client", nil, x509.ExtKeyUsageClientAuth)
	clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	require.NoError(t, err)

	tlsConf := &tls.Config{
		Certificates:       []tls.Certificate{clientCert},
		RootCAs:            ca.pool(),
		ServerName:         sni,
		NextProtos:         []string{tunnel.QuicALPN},
		ClientSessionCache: cache,
	}
	conn, err := quic.DialAddr(ctx, dialAddr, tlsConf, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	return conn
}

func (ca *testAgentCA) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	return pool
}

// testStreamConn adapts a *quic.Stream opened on conn to a net.Conn, exactly as
// production's dialAgent does for the agentpf client, so grpc.WithContextDialer can hand
// it to a *grpc.ClientConn.
type testStreamConn struct {
	*quic.Stream
	conn *quic.Conn
}

func (c *testStreamConn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *testStreamConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func (c *testStreamConn) Close() error {
	c.CancelRead(0)
	return c.Stream.Close()
}

// grpcCallOverStream makes one real Health/Check RPC over a QUIC stream dialed with
// tlsConf, proving the stream actually carries gRPC traffic end to end -- not just that
// the underlying QUIC handshake completed.
func grpcCallOverStream(t *testing.T, ctx context.Context, dialAddr string, tlsConf *tls.Config) {
	t.Helper()
	cc, err := grpcClient.DialGRPC(ctx, "passthrough:///"+dialAddr,
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			conn, err := quic.DialAddr(ctx, dialAddr, tlsConf, nil)
			if err != nil {
				return nil, err
			}
			qs, err := conn.OpenStreamSync(ctx)
			if err != nil {
				return nil, err
			}
			return &testStreamConn{Stream: qs, conn: conn}, nil
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer cc.Close()

	client := healthpb.NewHealthClient(cc)
	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.Status)
}

// TestListener_ResumesOnRedialWithSameMaterial verifies session resumption on redial
// with the same Material. getConfigForClient returns a brand new *tls.Config on every
// handshake (see its doc comment for why that does not fragment ticket-key state);
// this test is the empirical proof that resumption still succeeds across two dials
// through that Material-swapped GetConfigForClient path.
func TestListener_ResumesOnRedialWithSameMaterial(t *testing.T) {
	ctx, cancel := context.WithTimeout(testutil.NewContext(t, false), 10*time.Second)
	defer cancel()

	ca := newTestAgentCA(t)
	const sni = "agent-resume-test.example"
	serverCertPEM, serverKeyPEM := ca.mintLeafPEM(t, sni, []string{sni}, x509.ExtKeyUsageServerAuth)
	material, err := quicserver.ParseMaterial(serverCertPEM, serverKeyPEM, ca.pem)
	require.NoError(t, err)

	dialAddr := startTestListener(t, ctx, material)
	cache := &notifyingSessionCache{ClientSessionCache: tls.NewLRUClientSessionCache(16), put: make(chan struct{}, 1)}

	conn1 := dialAgent(t, ctx, ca, dialAddr, sni, cache)
	require.False(t, conn1.ConnectionState().TLS.DidResume, "the first dial has no ticket to resume")
	select {
	case <-cache.put:
	case <-ctx.Done():
		t.Fatal("timed out waiting for a session ticket to be cached")
	}
	require.NoError(t, conn1.CloseWithError(0, ""))

	conn2 := dialAgent(t, ctx, ca, dialAddr, sni, cache)
	require.True(t, conn2.ConnectionState().TLS.DidResume,
		"re-dial through the Material-swapped GetConfigForClient path must resume")
	require.NoError(t, conn2.CloseWithError(0, ""))

	// Prove a resumed connection's stream still carries real gRPC traffic end to end.
	resumedTLSConf := &tls.Config{
		Certificates:       conn2Certificates(t, ca),
		RootCAs:            ca.pool(),
		ServerName:         sni,
		NextProtos:         []string{tunnel.QuicALPN},
		ClientSessionCache: cache,
	}
	grpcCallOverStream(t, ctx, dialAddr, resumedTLSConf)
}

func conn2Certificates(t *testing.T, ca *testAgentCA) []tls.Certificate {
	t.Helper()
	certPEM, keyPEM := ca.mintLeafPEM(t, "client", nil, x509.ExtKeyUsageClientAuth)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	return []tls.Certificate{cert}
}

// TestListener_MaterialSwapFailsResumptionAcrossCAs documents why a SetMaterial swap
// (the agent re-fetching its certificate after a manager reconnect, which means a new
// signing CA) safely defeats resumption of a ticket minted under the old Material: a
// resumed ticket's embedded client certificate is independently re-verified against the
// Config's *current* ClientCAs, which no longer contains the old CA, so the ticket is
// rejected and a full handshake happens instead -- see getConfigForClient's doc comment.
func TestListener_MaterialSwapFailsResumptionAcrossCAs(t *testing.T) {
	ctx, cancel := context.WithTimeout(testutil.NewContext(t, false), 10*time.Second)
	defer cancel()

	ca1 := newTestAgentCA(t)
	const sni = "agent-resume-swap-test.example"
	serverCertPEM, serverKeyPEM := ca1.mintLeafPEM(t, sni, []string{sni}, x509.ExtKeyUsageServerAuth)
	material1, err := quicserver.ParseMaterial(serverCertPEM, serverKeyPEM, ca1.pem)
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	ln, err := quicserver.New(netip.MustParseAddr("10.42.3.10"), 0, grpcServer, material1)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = ln.Serve(ctx) }()
	udpAddr := ln.Addr().(*net.UDPAddr)
	dialAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(udpAddr.Port))

	cache := &notifyingSessionCache{ClientSessionCache: tls.NewLRUClientSessionCache(16), put: make(chan struct{}, 1)}
	conn1 := dialAgent(t, ctx, ca1, dialAddr, sni, cache)
	require.False(t, conn1.ConnectionState().TLS.DidResume)
	select {
	case <-cache.put:
	case <-ctx.Done():
		t.Fatal("timed out waiting for a session ticket to be cached")
	}
	require.NoError(t, conn1.CloseWithError(0, ""))

	// A new CA and server cert, as a manager restart would hand the agent, installed via
	// SetMaterial without restarting the listener.
	ca2 := newTestAgentCA(t)
	serverCertPEM2, serverKeyPEM2 := ca2.mintLeafPEM(t, sni, []string{sni}, x509.ExtKeyUsageServerAuth)
	material2, err := quicserver.ParseMaterial(serverCertPEM2, serverKeyPEM2, ca2.pem)
	require.NoError(t, err)
	ln.SetMaterial(material2)

	// The cached ticket's embedded certificate chains to ca1, which the listener no
	// longer accepts, so the client's RootCAs must now be ca2's for the dial to succeed
	// at all -- and that same CA change means the ticket's re-verification against the
	// listener's current ClientCAs (ca2) must fail, forcing a full handshake.
	clientCertPEM, clientKeyPEM := ca2.mintLeafPEM(t, "client", nil, x509.ExtKeyUsageClientAuth)
	clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	require.NoError(t, err)
	tlsConf := &tls.Config{
		Certificates:       []tls.Certificate{clientCert},
		RootCAs:            ca2.pool(),
		ServerName:         sni,
		NextProtos:         []string{tunnel.QuicALPN},
		ClientSessionCache: cache,
	}
	conn2, err := quic.DialAddr(ctx, dialAddr, tlsConf, nil)
	require.NoError(t, err)
	defer func() { _ = conn2.CloseWithError(0, "") }()
	require.False(t, conn2.ConnectionState().TLS.DidResume,
		"a ticket minted under the old Material's CA must not resume once SetMaterial has moved to a new CA")
}

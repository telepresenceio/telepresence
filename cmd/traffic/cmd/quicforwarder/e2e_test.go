package quicforwarder

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// generateQuicServerTLSConfig returns a bare-bones self-signed server TLS config for
// use with quic-go, configured for pkg/tunnel's QuicALPN -- mirroring
// pkg/tunnel/quic_test.go's generateQuicTLSConfig, since this test needs the same shape
// but lives in a different package.
func generateQuicServerTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, priv.Public(), priv)
	require.NoError(t, err)
	return &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{certDER},
			PrivateKey:  priv,
		}},
		NextProtos: []string{tunnel.QuicALPN},
	}
}

// echoBackend is a real quic-go server, standing in for "the manager as it will be
// configured behind the forwarder" per the task: a quic.Transport bound to a loopback
// UDP socket, minting connection IDs with pkg/quicfwd's CIDGenerator (so mid-connection
// packets route through the forwarder with no flow-table entry needed on its own,
// exactly like a real backend), presenting a certificate and ALPN a client dialing
// quicfwd.ManagerSNI expects, and echoing every stream's bytes back to the client.
type echoBackend struct {
	transport *quic.Transport
	ln        *quic.Listener
	ip        netip.Addr
	port      uint16
}

func startEchoBackend(t *testing.T) *echoBackend {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	ip := netip.MustParseAddr("127.0.0.1")
	transport := &quic.Transport{
		Conn:                  conn,
		ConnectionIDGenerator: quicfwd.NewCIDGenerator(ip),
	}
	ln, err := transport.Listen(generateQuicServerTLSConfig(t), &quic.Config{MaxIdleTimeout: 10 * time.Second})
	require.NoError(t, err)

	b := &echoBackend{
		transport: transport,
		ln:        ln,
		ip:        ip,
		port:      uint16(conn.LocalAddr().(*net.UDPAddr).Port),
	}
	go b.serve()
	return b
}

func (b *echoBackend) serve() {
	ctx := context.Background()
	for {
		conn, err := b.ln.Accept(ctx)
		if err != nil {
			return
		}
		go func() {
			for {
				s, err := conn.AcceptStream(ctx)
				if err != nil {
					return
				}
				go func() {
					_, _ = io.Copy(s, s)
					// io.Copy returns once it reads EOF (the client's
					// half-close), but never itself closes dst; without this,
					// the client's own EOF (via io.ReadAll) never arrives.
					_ = s.Close()
				}()
			}
		}()
	}
}

func (b *echoBackend) close() {
	_ = b.ln.Close()
	_ = b.transport.Close()
}

// dialClient dials the forwarder at fwdAddr presenting quicfwd.ManagerSNI, the SNI the
// forwarder must route to the echo backend on a connection's first packet.
func dialClient(t *testing.T, ctx context.Context, fwdAddr string) *quic.Conn { //nolint:revive // t first, matches other test helpers in this file
	t.Helper()
	tlsConf := &tls.Config{
		ServerName:         quicfwd.ManagerSNI,
		InsecureSkipVerify: true,
		NextProtos:         []string{tunnel.QuicALPN},
	}
	cfg := &quic.Config{MaxIdleTimeout: 10 * time.Second}
	conn, err := quic.DialAddr(ctx, fwdAddr, tlsConf, cfg)
	require.NoError(t, err)
	return conn
}

func exchange(t *testing.T, ctx context.Context, conn *quic.Conn, msg string) { //nolint:revive // t must be first for a test helper
	t.Helper()
	s, err := conn.OpenStreamSync(ctx)
	require.NoError(t, err)
	defer s.Close()

	_, err = s.Write([]byte(msg))
	require.NoError(t, err)
	require.NoError(t, s.Close())

	got, err := io.ReadAll(s)
	require.NoError(t, err)
	assert.Equal(t, msg, string(got))
}

// TestE2E_HandshakeThroughForwarder_BidirectionalData_ConcurrentSecondClient is the
// full end-to-end test the task calls for: a real quic-go client dials the forwarder
// presenting quicfwd.ManagerSNI; the forwarder's Initial-packet SNI routing (buffering
// across the multi-packet ClientHello split, per the handshake cache) and its
// CID-based routing for every packet after that must get the handshake all the way
// through to a real quic-go "manager" backend, indistinguishable (from the
// forwarder's point of view) from how the real manager will be configured: pod-IP
// listening (loopback here), a CIDGenerator-minted connection ID, and
// pkg/tunnel.QuicALPN. Once that connection is up, a second client dials while the
// first stays active, proving the forwarder's flow table keys correctly by source
// address rather than mixing the two up.
func TestE2E_HandshakeThroughForwarder_BidirectionalData_ConcurrentSecondClient(t *testing.T) {
	backend := startEchoBackend(t)
	defer backend.close()

	env := &Env{ListenPort: 0, BackendPort: backend.port}
	allowlist := NewAllowlist()
	allowlist.update(context.Background(), []*rpc.QuicBackend{{Ip: backend.ip.AsSlice(), Kind: "manager"}})

	fwd, err := Listen(env, allowlist)
	require.NoError(t, err)
	defer fwd.front.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- fwd.Serve(ctx) }()

	// Listen binds the wildcard address (matching production, where the
	// forwarder listens on all interfaces); dial loopback explicitly rather
	// than fwd.Addr().String(), which reports the wildcard host.
	fwdAddr := net.JoinHostPort("127.0.0.1", fmt.Sprint(fwd.front.LocalAddr().(*net.UDPAddr).Port))

	client1 := dialClient(t, ctx, fwdAddr)
	defer func() { _ = client1.CloseWithError(0, "") }()
	exchange(t, ctx, client1, "hello from client 1, round 1")

	// A second client connects while the first stays active: the flow table
	// must key by source address, not collapse the two into one flow.
	client2 := dialClient(t, ctx, fwdAddr)
	defer func() { _ = client2.CloseWithError(0, "") }()
	exchange(t, ctx, client2, "hello from client 2")

	// The first connection is still fully usable.
	exchange(t, ctx, client1, "hello from client 1, round 2")
	exchange(t, ctx, client2, "hello from client 2, round 2")

	assert.Equal(t, 2, fwd.flows.count())

	cancel()
	select {
	case err := <-serveErr:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after ctx cancellation")
	}
}

// TestE2E_ForwarderRestartSurvival exercises the design's central "stateless" claim
// directly: a forwarder that dies and comes back on the same port loses its flow
// table entirely, yet an established client<->backend QUIC connection resumes without
// redialing, because the client's packets after the restart carry the server-issued
// connection ID (routed by CID, no flow-table entry needed) and QUIC's own path
// validation absorbs what looks, from the backend's side, like the client migrating to
// a new path (a fresh forwarder-to-backend proxy connection has a different ephemeral
// source port).
func TestE2E_ForwarderRestartSurvival(t *testing.T) {
	backend := startEchoBackend(t)
	defer backend.close()

	allowlist := NewAllowlist()
	allowlist.update(context.Background(), []*rpc.QuicBackend{{Ip: backend.ip.AsSlice(), Kind: "manager"}})

	env1 := &Env{ListenPort: 0, BackendPort: backend.port}
	fwd1, err := Listen(env1, allowlist)
	require.NoError(t, err)
	port := fwd1.front.LocalAddr().(*net.UDPAddr).Port

	ctx1, cancel1 := context.WithCancel(context.Background())
	serve1Err := make(chan error, 1)
	go func() { serve1Err <- fwd1.Serve(ctx1) }()

	fwdAddr := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))

	dialCtx, dialCancel := context.WithCancel(context.Background())
	defer dialCancel()
	client := dialClient(t, dialCtx, fwdAddr)
	defer func() { _ = client.CloseWithError(0, "") }()
	exchange(t, dialCtx, client, "before restart")

	// Kill the forwarder and wait for its socket to be fully released before
	// rebinding the same port.
	cancel1()
	select {
	case <-serve1Err:
	case <-time.After(2 * time.Second):
		t.Fatal("first forwarder's Serve did not return")
	}

	env2 := &Env{ListenPort: uint16(port), BackendPort: backend.port}
	fwd2, err := Listen(env2, allowlist)
	require.NoError(t, err, "rebinding the same port after the first forwarder's socket closed")
	defer fwd2.front.Close()

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go func() { _ = fwd2.Serve(ctx2) }()

	// The same, never-redialed client connection must still work: its next
	// packet carries the server-issued CID, which the new forwarder instance
	// -- with no memory of the old flow table -- routes by CID alone.
	require.Eventually(t, func() bool {
		s, err := client.OpenStreamSync(dialCtx)
		if err != nil {
			return false
		}
		if _, err := s.Write([]byte("after restart")); err != nil {
			return false
		}
		if err := s.Close(); err != nil {
			return false
		}
		got, err := io.ReadAll(s)
		return err == nil && string(got) == "after restart"
	}, 5*time.Second, 50*time.Millisecond, "connection must resume through the new forwarder instance without redialing")
}

package quictunnel_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/quictunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// testPodIP is the pod IP every test listener in this file is configured with. Tests
// that dial the listener directly (bypassing the forwarder entirely) don't care what
// it is; TestListener_ServerCIDsEncodePodIP asserts that server-issued connection IDs
// decode back to exactly this value.
var testPodIP = netip.MustParseAddr("10.42.1.7")

func testContext(t *testing.T, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(testutil.NewContext(t, false), timeout)
}

// startTestListener starts a quictunnel.Listener with the given handler and returns
// it, its CA, and a func to dial it as an authenticated client for sessionID.
func startTestListener(t *testing.T, ctx context.Context, handler quictunnel.TunnelHandler) (*quictunnel.CA, string) {
	t.Helper()
	ca, err := quictunnel.NewCA()
	require.NoError(t, err)
	serverCert, err := ca.ServerTLSCert()
	require.NoError(t, err)

	ln, err := quictunnel.Listen(0, testPodIP, ca, serverCert, handler)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		_ = ln.Serve(ctx)
	}()

	udpAddr := ln.Addr().(*net.UDPAddr)
	dialAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(udpAddr.Port))
	return ca, dialAddr
}

func dialSession(t *testing.T, ctx context.Context, ca *quictunnel.CA, dialAddr, sessionID string) *quic.Conn {
	t.Helper()
	certPEM, keyPEM, err := ca.MintClientCert(sessionID)
	require.NoError(t, err)
	clientCert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      ca.Pool(),
		ServerName:   quictunnel.ServerName,
		NextProtos:   []string{tunnel.QuicALPN},
	}
	conn, err := quic.DialAddr(ctx, dialAddr, tlsConf, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })
	return conn
}

func TestListener_MatchingSessionIsServed(t *testing.T) {
	ctx, cancel := testContext(t, 10*time.Second)
	defer cancel()

	streamCh := make(chan tunnel.Stream, 1)
	handler := func(_ context.Context, s tunnel.Stream) error {
		streamCh <- s
		return nil
	}

	ca, dialAddr := startTestListener(t, ctx, handler)
	conn := dialSession(t, ctx, ca, dialAddr, "session-match")

	qs, err := conn.OpenStreamSync(ctx)
	require.NoError(t, err)

	id := tunnel.NewConnID(types.ProtoTCP,
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1001),
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 168, 0, 1}), 8080))
	client, err := tunnel.NewClientStream(ctx, tunnel.ClientToManager, tunnel.NewQuicClientStream(conn, qs),
		id, tunnel.SessionID("session-match"), 0, 0)
	require.NoError(t, err)

	select {
	case s := <-streamCh:
		require.Equal(t, tunnel.SessionID("session-match"), s.SessionID())
	case <-ctx.Done():
		t.Fatal("timed out waiting for handler to be invoked")
	}
	require.NoError(t, client.CloseSend(ctx))
}

func TestListener_MismatchedSessionIsRejected(t *testing.T) {
	ctx, cancel := testContext(t, 10*time.Second)
	defer cancel()

	streamCh := make(chan tunnel.Stream, 1)
	handler := func(_ context.Context, s tunnel.Stream) error {
		streamCh <- s
		return nil
	}

	// The client authenticates as "cert-session" but declares a different session ID
	// in its StreamInfo message; the listener must reject the stream without ever
	// invoking handler.
	ca, dialAddr := startTestListener(t, ctx, handler)
	conn := dialSession(t, ctx, ca, dialAddr, "cert-session")

	qs, err := conn.OpenStreamSync(ctx)
	require.NoError(t, err)

	id := tunnel.NewConnID(types.ProtoTCP,
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1001),
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 168, 0, 1}), 8080))
	client, err := tunnel.NewClientStream(ctx, tunnel.ClientToManager, tunnel.NewQuicClientStream(conn, qs),
		id, tunnel.SessionID("declared-session"), 0, 0)
	// The listener resets the stream as soon as it observes the session mismatch,
	// which races with the StreamOK reply already written for the handshake: either
	// the reset overtakes the reply (NewClientStream itself fails) or it doesn't
	// (NewClientStream succeeds but any further use of the stream fails).
	if err == nil {
		_, err = client.Receive(ctx)
	}
	require.Error(t, err)

	select {
	case <-streamCh:
		t.Fatal("handler must not be invoked for a session ID that doesn't match the client certificate")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestListener_UnauthenticatedClientRejected(t *testing.T) {
	ctx, cancel := testContext(t, 10*time.Second)
	defer cancel()

	handler := func(_ context.Context, _ tunnel.Stream) error {
		t.Fatal("handler must not be invoked for an unauthenticated connection")
		return nil
	}

	ca, dialAddr := startTestListener(t, ctx, handler)

	// A client with no certificate at all fails TLS's client-certificate
	// requirement. quic-go's DialAddr can return successfully before that failure
	// is delivered (it arrives as a CRYPTO_ERROR on first stream use), so the
	// rejection must be observed through the stream, not through DialAddr's error.
	tlsConf := &tls.Config{
		RootCAs:    ca.Pool(),
		ServerName: quictunnel.ServerName,
		NextProtos: []string{tunnel.QuicALPN},
	}
	conn, err := quic.DialAddr(ctx, dialAddr, tlsConf, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseWithError(0, "") })

	qs, err := conn.OpenStreamSync(ctx)
	require.NoError(t, err)
	_, err = qs.Write([]byte("x"))
	if err == nil {
		_, err = qs.Read(make([]byte, 1))
	}
	require.Error(t, err)
}

// capturedPackets is a concurrency-safe recorder for raw UDP payloads observed by a
// capturingProxy, in arrival order.
type capturedPackets struct {
	mu   sync.Mutex
	pkts [][]byte
}

func (c *capturedPackets) add(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pkts = append(c.pkts, append([]byte(nil), p...))
}

func (c *capturedPackets) snapshot() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]byte(nil), c.pkts...)
}

// startCapturingProxy starts a transparent UDP relay in front of serverAddr and returns
// its own address (what a test should dial instead of serverAddr) plus every packet it
// forwarded in the client->server direction. quic-go's public API exposes no accessor
// for a connection's negotiated connection IDs, so this is the only way to observe the
// server-issued CIDs the dialing client ends up using as DCID on outgoing packets --
// which is exactly what the forwarder routes on (see quicfwd.ParsePacket).
func startCapturingProxy(t *testing.T, ctx context.Context, serverAddr string) (proxyAddr string, captured *capturedPackets) {
	t.Helper()
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = front.Close() })

	udpServerAddr, err := net.ResolveUDPAddr("udp", serverAddr)
	require.NoError(t, err)
	back, err := net.DialUDP("udp", nil, udpServerAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = back.Close() })

	captured = &capturedPackets{}
	var clientAddrMu sync.Mutex
	var clientAddr *net.UDPAddr

	go func() {
		<-ctx.Done()
		_ = front.Close()
		_ = back.Close()
	}()

	// client -> server: record, then forward unchanged.
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := front.ReadFromUDP(buf)
			if err != nil {
				return
			}
			clientAddrMu.Lock()
			clientAddr = addr
			clientAddrMu.Unlock()
			captured.add(buf[:n])
			_, _ = back.Write(buf[:n])
		}
	}()

	// server -> client: forward unchanged, uninspected (only the client->server
	// direction carries the DCID the client learned from the server).
	go func() {
		buf := make([]byte, 2048)
		for {
			n, err := back.Read(buf)
			if err != nil {
				return
			}
			clientAddrMu.Lock()
			dst := clientAddr
			clientAddrMu.Unlock()
			if dst != nil {
				_, _ = front.WriteToUDP(buf[:n], dst)
			}
		}
	}()

	return front.LocalAddr().String(), captured
}

// TestListener_ServerCIDsEncodePodIP is the wire-level proof, from the manager side,
// that the listener is actually wired up with a quicfwd.CIDGenerator for its configured
// pod IP: it relays a real client dial through a capturing proxy and inspects the raw
// short-header packets the client sends back to the server, decoding each one's DCID --
// the connection ID the server issued -- via quicfwd.DecodeCID. Direct-dial tests
// elsewhere in this file bypass the proxy entirely and are unaffected by CID routing,
// since QUIC doesn't care how its UDP packets get from client to server.
func TestListener_ServerCIDsEncodePodIP(t *testing.T) {
	ctx, cancel := testContext(t, 10*time.Second)
	defer cancel()

	streamCh := make(chan tunnel.Stream, 1)
	handler := func(_ context.Context, s tunnel.Stream) error {
		streamCh <- s
		return nil
	}

	ca, dialAddr := startTestListener(t, ctx, handler)
	proxyAddr, captured := startCapturingProxy(t, ctx, dialAddr)

	conn := dialSession(t, ctx, ca, proxyAddr, "session-cid")
	qs, err := conn.OpenStreamSync(ctx)
	require.NoError(t, err)

	id := tunnel.NewConnID(types.ProtoTCP,
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1001),
		netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 168, 0, 1}), 8080))
	client, err := tunnel.NewClientStream(ctx, tunnel.ClientToManager, tunnel.NewQuicClientStream(conn, qs),
		id, tunnel.SessionID("session-cid"), 0, 0)
	require.NoError(t, err)

	select {
	case <-streamCh:
	case <-ctx.Done():
		t.Fatal("timed out waiting for handler to be invoked")
	}
	require.NoError(t, client.CloseSend(ctx))

	// The handshake having completed and a stream having been opened guarantees at
	// least one 1-RTT (short-header) packet was sent client->server, but the capture
	// goroutine races with this goroutine, so poll briefly rather than inspecting
	// captured.snapshot() exactly once.
	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for !found && time.Now().Before(deadline) {
		for _, pkt := range captured.snapshot() {
			info, err := quicfwd.ParsePacket(pkt)
			if err != nil || info.Kind != quicfwd.KindShortHeader {
				continue
			}
			ip, ok := quicfwd.DecodeCID(info.DCID)
			if !ok {
				continue
			}
			require.Equal(t, testPodIP, ip)
			found = true
			break
		}
		if !found {
			time.Sleep(20 * time.Millisecond)
		}
	}
	require.True(t, found, "expected at least one short-header packet whose DCID decodes to the configured pod IP")
}

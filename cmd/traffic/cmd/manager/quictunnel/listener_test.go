package quictunnel_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/quictunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

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

	ln, err := quictunnel.Listen(0, ca, serverCert, handler)
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
	client, err := tunnel.NewClientStream(ctx, tunnel.ClientToManager, tunnel.NewQuicClientStream(qs),
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
	client, err := tunnel.NewClientStream(ctx, tunnel.ClientToManager, tunnel.NewQuicClientStream(qs),
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

package agentpf

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
)

// fakeManagerClient embeds the (nil) manager.ManagerClient interface and overrides only
// GetQuicTunnelEndpoint, so tests don't have to stub the whole (large) interface. Calling
// any other method panics on the nil embedded value, which is the point: these tests must
// never exercise anything but the QUIC endpoint fetch.
type fakeManagerClient struct {
	manager.ManagerClient
	calls int32
	ep    *manager.QuicTunnelEndpoint
	err   error
}

func (f *fakeManagerClient) GetQuicTunnelEndpoint(context.Context, *manager.SessionInfo, ...grpc.CallOption) (*manager.QuicTunnelEndpoint, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.ep, f.err
}

func TestQuicEndpointCache_NegativeCacheUnimplemented(t *testing.T) {
	fc := &fakeManagerClient{err: status.Error(codes.Unimplemented, "no such method")}
	c := &quicEndpointCache{}
	session := &manager.SessionInfo{SessionId: "s"}

	ep1 := c.get(context.Background(), fc, session)
	ep2 := c.get(context.Background(), fc, session)

	assert.Nil(t, ep1)
	assert.Nil(t, ep2)
	assert.EqualValues(t, 1, atomic.LoadInt32(&fc.calls), "GetQuicTunnelEndpoint must be called at most once per session")
}

func TestQuicEndpointCache_NegativeCacheDisabled(t *testing.T) {
	fc := &fakeManagerClient{ep: &manager.QuicTunnelEndpoint{Enabled: false}}
	c := &quicEndpointCache{}
	session := &manager.SessionInfo{SessionId: "s"}

	require.Nil(t, c.get(context.Background(), fc, session))
	require.Nil(t, c.get(context.Background(), fc, session))
	assert.EqualValues(t, 1, atomic.LoadInt32(&fc.calls))
}

func TestQuicEndpointCache_NilManagerClientNotCached(t *testing.T) {
	c := &quicEndpointCache{}
	session := &manager.SessionInfo{SessionId: "s"}

	// No manager client available yet (e.g. WatchAgentPods hasn't started): the fetch
	// must be skipped, not cached as a permanent negative.
	require.Nil(t, c.get(context.Background(), nil, session))

	certPEM, keyPEM := testCertAndKeyPEM(t)
	fc := &fakeManagerClient{ep: &manager.QuicTunnelEndpoint{
		Enabled:       true,
		Host:          "127.0.0.1",
		Port:          4443,
		CaPem:         certPEM,
		ClientCertPem: certPEM,
		ClientKeyPem:  keyPEM,
	}}
	ep := c.get(context.Background(), fc, session)
	require.NotNil(t, ep)
	assert.EqualValues(t, 1, atomic.LoadInt32(&fc.calls))

	// Now cached: a later call, even with a client that would fail, must not re-fetch.
	fc2 := &fakeManagerClient{err: errors.New("must not be called")}
	ep2 := c.get(context.Background(), fc2, session)
	assert.Same(t, ep, ep2)
	assert.EqualValues(t, 0, atomic.LoadInt32(&fc2.calls))
}

func TestQuicEndpointCache_BadCertNotUsable(t *testing.T) {
	_, keyPEM := testCertAndKeyPEM(t)
	fc := &fakeManagerClient{ep: &manager.QuicTunnelEndpoint{
		Enabled:       true,
		Host:          "127.0.0.1",
		Port:          4443,
		CaPem:         []byte("not a cert"),
		ClientCertPem: []byte("not a cert either"),
		ClientKeyPem:  keyPEM,
	}}
	c := &quicEndpointCache{}
	require.Nil(t, c.get(context.Background(), fc, &manager.SessionInfo{SessionId: "s"}))
}

// --- agentDialer decision logic -------------------------------------------------------

func TestAgentDialer_EmptySniGoesStraightToPortForward(t *testing.T) {
	var quicDead atomic.Bool
	var transport atomic.Value
	epForCalled := false
	dialQuicCalled := false
	dialFallbackCalled := false

	dialer := agentDialer("", &quicDead, &transport,
		func(context.Context) *quicEndpoint { epForCalled = true; return &quicEndpoint{} },
		func(context.Context, *quicEndpoint, string) (net.Conn, error) { dialQuicCalled = true; return nil, nil },
		func(context.Context, string) (net.Conn, error) { dialFallbackCalled = true; return nil, nil },
		nil)

	_, err := dialer(context.Background(), "addr")
	require.NoError(t, err)
	assert.False(t, epForCalled, "the descriptor must not be fetched for an agent with no quic_sni")
	assert.False(t, dialQuicCalled)
	assert.True(t, dialFallbackCalled)
	assert.Equal(t, transportPortForward, transport.Load())
	assert.False(t, quicDead.Load())
}

func TestAgentDialer_NoEndpointGoesToPortForward(t *testing.T) {
	var quicDead atomic.Bool
	var transport atomic.Value
	dialQuicCalled := false

	dialer := agentDialer("agent.sni", &quicDead, &transport,
		func(context.Context) *quicEndpoint { return nil }, // disabled/unimplemented/negative-cached
		func(context.Context, *quicEndpoint, string) (net.Conn, error) { dialQuicCalled = true; return nil, nil },
		func(context.Context, string) (net.Conn, error) { return nil, nil },
		nil)

	_, err := dialer(context.Background(), "addr")
	require.NoError(t, err)
	assert.False(t, dialQuicCalled)
	assert.Equal(t, transportPortForward, transport.Load())
	assert.False(t, quicDead.Load(), "no endpoint is not a QUIC failure")
}

func TestAgentDialer_QuicSuccess(t *testing.T) {
	var quicDead atomic.Bool
	var transport atomic.Value
	wantConn := &net.TCPConn{}
	fallbackCalled := false

	dialer := agentDialer("agent.sni", &quicDead, &transport,
		func(context.Context) *quicEndpoint { return &quicEndpoint{} },
		func(context.Context, *quicEndpoint, string) (net.Conn, error) { return wantConn, nil },
		func(context.Context, string) (net.Conn, error) { fallbackCalled = true; return nil, nil },
		nil)

	got, err := dialer(context.Background(), "addr")
	require.NoError(t, err)
	assert.Same(t, net.Conn(wantConn), got)
	assert.False(t, fallbackCalled)
	assert.Equal(t, transportQUIC, transport.Load())
	assert.False(t, quicDead.Load())
}

func TestAgentDialer_QuicFailureMarksDeadLogsOnceAndFallsBack(t *testing.T) {
	var quicDead atomic.Bool
	var transport atomic.Value
	quicAttempts := 0
	logCalls := 0

	dialer := agentDialer("agent.sni", &quicDead, &transport,
		func(context.Context) *quicEndpoint { return &quicEndpoint{} },
		func(context.Context, *quicEndpoint, string) (net.Conn, error) {
			quicAttempts++
			return nil, errors.New("dial failed")
		},
		func(context.Context, string) (net.Conn, error) { return nil, nil },
		func(error) { logCalls++ })

	// First attempt: tries QUIC, fails, marks dead, logs once, falls back.
	_, err := dialer(context.Background(), "addr")
	require.NoError(t, err)
	assert.True(t, quicDead.Load())
	assert.Equal(t, transportPortForward, transport.Load())
	assert.Equal(t, 1, quicAttempts)
	assert.Equal(t, 1, logCalls)

	// A later invocation of the SAME dialer (as grpc's own reconnect would make) must not
	// retry QUIC, and must not log again.
	_, err = dialer(context.Background(), "addr")
	require.NoError(t, err)
	assert.Equal(t, 1, quicAttempts, "QUIC must not be retried once dead")
	assert.Equal(t, 1, logCalls, "the failure must be logged exactly once")
}

// --- client.refresh --------------------------------------------------------------------

func TestClientRefresh_ResetsQuicDead(t *testing.T) {
	cl := &k8s.Cluster{
		Kubeconfig: &k8s.Kubeconfig{
			Context:   context.Background(),
			Namespace: "alpha",
		},
	}
	oldInfo := &manager.AgentPodInfo{PodName: "agent", Namespace: "alpha", Intercepted: false}
	ac := &client{Cluster: cl, session: &manager.SessionInfo{SessionId: "s"}, info: oldInfo}
	ac.quicDead.Store(true)

	newInfo := &manager.AgentPodInfo{PodName: "agent", Namespace: "alpha", Intercepted: false, PodId: "new-uid"}
	ac.refresh(newInfo)

	assert.False(t, ac.quicDead.Load(), "an AgentPodInfo update must give QUIC a fresh chance")
	assert.Same(t, newInfo, ac.info)
}

// --- loopback QUIC round-trip through the net.Conn adapter -----------------------------

func TestQuicStreamConn_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ln, err := quic.ListenAddr("127.0.0.1:0", generateTestQuicTLSConfig(t), nil)
	require.NoError(t, err)
	defer ln.Close()

	serverConnCh := make(chan net.Conn, 1)
	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept(ctx)
		if err != nil {
			serverErrCh <- err
			return
		}
		s, err := conn.AcceptStream(ctx)
		if err != nil {
			serverErrCh <- err
			return
		}
		serverConnCh <- newQuicStreamConn(s, conn)
	}()

	clientTLSConf := &tls.Config{
		InsecureSkipVerify: true, // no CA to verify against in this loopback test
		NextProtos:         []string{"quic-test"},
	}
	conn, err := quic.DialAddr(ctx, ln.Addr().String(), clientTLSConf, nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseWithError(0, "") }()

	stream, err := conn.OpenStreamSync(ctx)
	require.NoError(t, err)
	clientConn := newQuicStreamConn(stream, conn)

	require.NotNil(t, clientConn.LocalAddr())
	require.NotNil(t, clientConn.RemoteAddr())

	// A QUIC stream is only observed by the peer's AcceptStream once data actually flows
	// on it, so the first write must happen before waiting for the server side to show up.
	_, err = clientConn.Write([]byte("ping"))
	require.NoError(t, err)

	var serverConn net.Conn
	select {
	case serverConn = <-serverConnCh:
	case err := <-serverErrCh:
		t.Fatalf("server side failed: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for server stream")
	}

	buf := make([]byte, 4)
	_, err = io.ReadFull(serverConn, buf)
	require.NoError(t, err)
	assert.Equal(t, "ping", string(buf))

	_, err = serverConn.Write([]byte("pong"))
	require.NoError(t, err)
	_, err = io.ReadFull(clientConn, buf)
	require.NoError(t, err)
	assert.Equal(t, "pong", string(buf))
}

// generateTestQuicTLSConfig returns a bare-bones self-signed server TLS config for use with
// quic-go in a loopback test.
func generateTestQuicTLSConfig(t *testing.T) *tls.Config {
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
		NextProtos: []string{"quic-test"},
	}
}

// testCertAndKeyPEM generates a fresh self-signed certificate and its matching private key,
// both PEM-encoded.
func testCertAndKeyPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, pub, priv)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

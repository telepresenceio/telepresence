package rootd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/usg"
)

// fakeTunnelProvider is a tunnel.Provider whose Tunnel() call is scripted by the test:
// it returns errs[n] (or a distinct fake stream when nil) on its n-th invocation, and
// records how many times it was called.
type fakeTunnelProvider struct {
	name  string
	errs  []error
	calls int
}

func (f *fakeTunnelProvider) Tunnel(context.Context, ...grpc.CallOption) (tunnel.GRPCClientStream, error) {
	i := f.calls
	f.calls++
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	if err != nil {
		return nil, err
	}
	return fakeGRPCClientStream{f.name}, nil
}

// fakeGRPCClientStream is a distinguishable no-op implementation of tunnel.GRPCClientStream.
type fakeGRPCClientStream struct {
	from string
}

func (fakeGRPCClientStream) Recv() (*rpc.TunnelMessage, error) { return nil, nil }
func (fakeGRPCClientStream) Send(*rpc.TunnelMessage) error     { return nil }
func (fakeGRPCClientStream) CloseSend() error                  { return nil }

// fakeConnCloser records whether CloseWithError was called.
type fakeConnCloser struct {
	closed int
}

func (f *fakeConnCloser) CloseWithError(quic.ApplicationErrorCode, string) error {
	f.closed++
	return nil
}

func TestQuicFallbackProvider_HealthyPathUsesQuic(t *testing.T) {
	quicP := &fakeTunnelProvider{name: "quic"}
	fallbackP := &fakeTunnelProvider{name: "grpc"}
	conn := &fakeConnCloser{}

	p := newQuicFallbackProvider(context.Background(), quicP, func() tunnel.Provider { return fallbackP }, conn, nil)

	st, err := p.Tunnel(context.Background())
	require.NoError(t, err)
	require.Equal(t, "quic", st.(fakeGRPCClientStream).from)
	require.Equal(t, 1, quicP.calls)
	require.Equal(t, 0, fallbackP.calls)
	require.Equal(t, 0, conn.closed)
	require.False(t, p.dead.Load())

	// A second healthy call still goes to QUIC.
	st, err = p.Tunnel(context.Background())
	require.NoError(t, err)
	require.Equal(t, "quic", st.(fakeGRPCClientStream).from)
	require.Equal(t, 2, quicP.calls)
	require.Equal(t, 0, fallbackP.calls)
}

func TestQuicFallbackProvider_ErrorSwitchesToFallbackPermanently(t *testing.T) {
	quicErr := errors.New("quic: connection lost")
	quicP := &fakeTunnelProvider{name: "quic", errs: []error{quicErr}}
	fallbackP := &fakeTunnelProvider{name: "grpc"}
	conn := &fakeConnCloser{}
	fallbackCalls := 0

	p := newQuicFallbackProvider(context.Background(), quicP, func() tunnel.Provider { return fallbackP }, conn, func() {
		fallbackCalls++
	})

	// First call: QUIC fails, the same call must be retried and succeed via the fallback.
	st, err := p.Tunnel(context.Background())
	require.NoError(t, err)
	require.Equal(t, "grpc", st.(fakeGRPCClientStream).from)
	require.Equal(t, 1, quicP.calls)
	require.Equal(t, 1, fallbackP.calls)
	require.True(t, p.dead.Load())
	require.Equal(t, 1, conn.closed)
	require.Equal(t, 1, fallbackCalls, "onFallback must run exactly once")

	// Subsequent calls go straight to the fallback; QUIC is never retried.
	st, err = p.Tunnel(context.Background())
	require.NoError(t, err)
	require.Equal(t, "grpc", st.(fakeGRPCClientStream).from)
	require.Equal(t, 1, quicP.calls)
	require.Equal(t, 2, fallbackP.calls)
	require.Equal(t, 1, conn.closed, "the QUIC connection must only be closed once")
	require.Equal(t, 1, fallbackCalls, "onFallback must not run again on later calls")
}

func TestQuicFallbackProvider_CallerCancellationIsNotAFailure(t *testing.T) {
	quicP := &fakeTunnelProvider{name: "quic", errs: []error{context.Canceled}}
	fallbackP := &fakeTunnelProvider{name: "grpc"}
	conn := &fakeConnCloser{}

	p := newQuicFallbackProvider(context.Background(), quicP, func() tunnel.Provider { return fallbackP }, conn, func() {
		t.Fatal("onFallback must not run for caller cancellation")
	})

	// A stream whose own context is already canceled fails, but must neither mark
	// QUIC dead nor be retried on the fallback.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := p.Tunnel(ctx)
	require.Error(t, err)
	require.Equal(t, 1, quicP.calls)
	require.Equal(t, 0, fallbackP.calls)
	require.Equal(t, 0, conn.closed)
	require.False(t, p.dead.Load())

	// The next healthy call still uses QUIC.
	st, err := p.Tunnel(context.Background())
	require.NoError(t, err)
	require.Equal(t, "quic", st.(fakeGRPCClientStream).from)
	require.Equal(t, 2, quicP.calls)
	require.Equal(t, 0, fallbackP.calls)
}

// TestSession_TransportStatus_DefaultsToGRPC verifies the steady state most sessions
// are in: no QUIC dial ever happened, so TransportStatus reports plain "grpc" with no
// endpoint, even though transportStatus was never explicitly set.
func TestSession_TransportStatus_DefaultsToGRPC(t *testing.T) {
	s := &session{}
	transport, endpoint := s.TransportStatus()
	assert.Equal(t, TransportGRPC, transport)
	assert.Empty(t, endpoint)
}

// TestSession_SetTransportStatus verifies that setTransportStatus is what
// TransportStatus subsequently observes, for both the "quic" (with endpoint) and
// "grpc (fallback)" (without one) cases.
func TestSession_SetTransportStatus(t *testing.T) {
	s := &session{}

	s.setTransportStatus(TransportQUIC, "1.2.3.4:7778")
	transport, endpoint := s.TransportStatus()
	assert.Equal(t, TransportQUIC, transport)
	assert.Equal(t, "1.2.3.4:7778", endpoint)

	s.setTransportStatus(TransportGRPCFallback, "")
	transport, endpoint = s.TransportStatus()
	assert.Equal(t, TransportGRPCFallback, transport)
	assert.Empty(t, endpoint)
}

// TestReportTransport verifies the shape of the session.transport usage report: no
// "reason" entry (and hence no chance of it being confused with a real value) on the
// successful-QUIC report, and both "transport" and "reason" on every grpc outcome.
// Critically, neither call ever carries the endpoint address (see the usg package doc
// on what's safe to report).
func TestReportTransport(t *testing.T) {
	ctx, sink := usg.InstallManager(t.Context(), "test-install")

	reportTransport(ctx, TransportQUIC, "")
	reportTransport(ctx, TransportGRPC, "dial-failed")

	reports := sink.Drain(0)
	require.Len(t, reports, 2)

	assert.Equal(t, transportUsageTopic, reports[0].Topic)
	assert.Equal(t, "quic", reports[0].Entries["transport"])
	_, hasReason := reports[0].Entries["reason"]
	assert.False(t, hasReason, "a successful QUIC dial must not carry a reason entry")

	assert.Equal(t, transportUsageTopic, reports[1].Topic)
	assert.Equal(t, "grpc", reports[1].Entries["transport"])
	assert.Equal(t, "dial-failed", reports[1].Entries["reason"])

	for _, r := range reports {
		for _, v := range r.Entries {
			assert.NotContains(t, v, ".", "no report entry should carry an address-shaped value")
		}
	}
}

// TestQuicFallbackProvider_OnFallbackUpdatesTransportStatus is an integration-shaped
// test of the wiring startQuicTunnel sets up: the onFallback callback both flips the
// session's observable transport state and emits the usage report, exactly once.
func TestQuicFallbackProvider_OnFallbackUpdatesTransportStatus(t *testing.T) {
	ctx, sink := usg.InstallManager(t.Context(), "test-install")
	s := &session{}
	s.setTransportStatus(TransportQUIC, "1.2.3.4:7778")

	quicP := &fakeTunnelProvider{name: "quic", errs: []error{errors.New("quic: connection lost")}}
	fallbackP := &fakeTunnelProvider{name: "grpc"}
	conn := &fakeConnCloser{}

	p := newQuicFallbackProvider(ctx, quicP, func() tunnel.Provider { return fallbackP }, conn, func() {
		s.setTransportStatus(TransportGRPCFallback, "")
		reportTransport(ctx, TransportGRPC, "fallback")
	})

	_, err := p.Tunnel(context.Background())
	require.NoError(t, err)

	transport, endpoint := s.TransportStatus()
	assert.Equal(t, TransportGRPCFallback, transport)
	assert.Empty(t, endpoint)

	reports := sink.Drain(0)
	require.Len(t, reports, 1)
	assert.Equal(t, transportUsageTopic, reports[0].Topic)
	assert.Equal(t, "grpc", reports[0].Entries["transport"])
	assert.Equal(t, "fallback", reports[0].Entries["reason"])

	// A second failure must not re-trigger onFallback (dead is already true).
	_, err = p.Tunnel(context.Background())
	require.NoError(t, err)
	assert.Len(t, sink.Drain(0), 0, "onFallback (and its usage report) must fire exactly once")
}

// --- candidate list and concurrent probe ------------------------------------------------

func TestQuicCandidateAddrs_FallsBackToHostPort(t *testing.T) {
	ep := &rpc.QuicTunnelEndpoint{Host: "203.0.113.9", Port: 7778}
	assert.Equal(t, []string{"203.0.113.9:7778"}, quicCandidateAddrs(ep))
}

func TestQuicCandidateAddrs_UsesCandidatesWhenPresent(t *testing.T) {
	ep := &rpc.QuicTunnelEndpoint{
		Host: "203.0.113.9", Port: 7778, // older-manager-compatible duplicate of candidates[0]
		Candidates: []*rpc.QuicEndpointCandidate{
			{Host: "203.0.113.9", Port: 7778},
			{Host: "198.51.100.5", Port: 31778},
		},
	}
	assert.Equal(t, []string{"203.0.113.9:7778", "198.51.100.5:31778"}, quicCandidateAddrs(ep))
}

// generateTestQuicTLSConfig returns a bare-bones self-signed server TLS config for use
// with quic-go in a loopback test.
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
		NextProtos: []string{"quic-candidate-test"},
	}
}

// startTestQuicListener starts a bare quic-go listener on loopback and returns its dial
// address; the caller is responsible for accepting connections if it cares to.
func startTestQuicListener(t *testing.T, ctx context.Context) string {
	t.Helper()
	ln, err := quic.ListenAddr("127.0.0.1:0", generateTestQuicTLSConfig(t), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			go func() { _ = conn.CloseWithError(0, "") }()
		}
	}()
	return ln.Addr().String()
}

func clientQuicTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, // no CA to verify against in this loopback test
		NextProtos:         []string{"quic-candidate-test"},
	}
}

func TestDialQuicCandidates_PicksTheOnlyReachableCandidate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	live := startTestQuicListener(t, ctx)
	// Port 0 is never dialable; it fails immediately rather than timing out, so this
	// case exercises "one candidate fails, the other succeeds" without waiting out a
	// full stagger interval.
	conn, addr, err := dialQuicCandidates(ctx, []string{"127.0.0.1:0", live}, clientQuicTLSConfig(), nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseWithError(0, "") }()
	assert.Equal(t, live, addr)
}

func TestDialQuicCandidates_PreferredCandidateWinsWithoutWaitingForStagger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	live := startTestQuicListener(t, ctx)
	// live is listed first; a second, reachable-but-slower-to-start candidate must not
	// delay the result past roughly one handshake, proving the preferred candidate's
	// success is returned without waiting for every candidate to resolve.
	start := time.Now()
	conn, addr, err := dialQuicCandidates(ctx, []string{live, "127.0.0.1:0"}, clientQuicTLSConfig(), nil)
	elapsed := time.Since(start)
	require.NoError(t, err)
	defer func() { _ = conn.CloseWithError(0, "") }()
	assert.Equal(t, live, addr)
	assert.Less(t, elapsed, quicCandidateStagger, "the first candidate's success must not wait for the second's stagger delay")
}

func TestDialQuicCandidates_AllUnreachableReturnsError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, _, err := dialQuicCandidates(ctx, []string{"127.0.0.1:0", "127.0.0.1:0"}, clientQuicTLSConfig(), nil)
	require.Error(t, err)
}

package rootd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/agentpf"
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

	reportTransport(ctx, TransportQUIC, "", false)
	reportTransport(ctx, TransportGRPC, "dial-failed", false)

	reports := sink.Drain(0)
	require.Len(t, reports, 2)

	assert.Equal(t, transportUsageTopic, reports[0].Topic)
	assert.Equal(t, "quic", reports[0].Entries["transport"])
	_, hasReason := reports[0].Entries["reason"]
	assert.False(t, hasReason, "a successful QUIC dial must not carry a reason entry")
	_, hasResumed := reports[0].Entries["resumed"]
	assert.False(t, hasResumed, "a non-resumed dial must not carry a resumed entry")

	assert.Equal(t, transportUsageTopic, reports[1].Topic)
	assert.Equal(t, "grpc", reports[1].Entries["transport"])
	assert.Equal(t, "dial-failed", reports[1].Entries["reason"])

	for _, r := range reports {
		for _, v := range r.Entries {
			assert.NotContains(t, v, ".", "no report entry should carry an address-shaped value")
		}
	}
}

// TestReportTransport_Resumed verifies that resumed is only ever reported as "true",
// never spelled out as "false", so its mere presence in field data answers whether
// resumption happened.
func TestReportTransport_Resumed(t *testing.T) {
	ctx, sink := usg.InstallManager(t.Context(), "test-install")

	reportTransport(ctx, TransportQUIC, "reprobe", true)

	reports := sink.Drain(0)
	require.Len(t, reports, 1)
	assert.Equal(t, "reprobe", reports[0].Entries["reason"])
	assert.Equal(t, "true", reports[0].Entries["resumed"])
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
		reportTransport(ctx, TransportGRPC, "fallback", false)
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

// --- re-probe ------------------------------------------------------------------------

// fakeAgentClients is an agentpf.Clients that only implements ResetQuicEndpoint, by
// embedding a nil agentpf.Clients: attemptReprobe only ever calls that one method on
// session.agentClients, so a call to anything else here panics loudly rather than
// silently succeeding.
type fakeAgentClients struct {
	agentpf.Clients
	resets int
}

func (f *fakeAgentClients) ResetQuicEndpoint() { f.resets++ }

// TestSession_QuicReprobe_RecoversAfterFallbackTrip is the unit-level proof of the
// re-probe design: a quicFallbackProvider trips into fallback through the exact
// onFallback callback production wires up (onQuicFallback), which wakes the reprobe
// trigger; a fake prober is then retried exactly as quicReprobeLoop would retry it
// (advancing the probe by calling attemptReprobe directly, once per simulated tick, with
// no real timer involved); and once it succeeds the session's active provider swaps back
// to serve QUIC. Both the fallback usage report and the recovery usage report must fire
// exactly once, for their one respective transition.
func TestSession_QuicReprobe_RecoversAfterFallbackTrip(t *testing.T) {
	ctx, sink := usg.InstallManager(t.Context(), "test-install")
	agents := &fakeAgentClients{}
	s := &session{agentClients: agents, quicReprobeTrigger: make(chan struct{}, 1)}
	s.setTransportStatus(TransportQUIC, "198.51.100.1:7778")

	// Trip: the same failure path a real quicFallbackProvider takes when its Tunnel()
	// call fails, wired with the production onFallback callback.
	quicErr := errors.New("quic: connection lost")
	quicP := &fakeTunnelProvider{name: "quic", errs: []error{quicErr}}
	fallbackP := &fakeTunnelProvider{name: "grpc"}
	conn := &fakeConnCloser{}
	trippedProvider := newQuicFallbackProvider(ctx, quicP, func() tunnel.Provider { return fallbackP }, conn, s.onQuicFallback(ctx))
	s.quicTunnelProvider.Store(trippedProvider)

	_, err := trippedProvider.Tunnel(context.Background())
	require.NoError(t, err) // served by the fallback

	transport, _ := s.TransportStatus()
	require.Equal(t, TransportGRPCFallback, transport, "the trip must have downgraded transport status")
	select {
	case <-s.quicReprobeTrigger:
	default:
		t.Fatal("the trip must have woken the reprobe loop")
	}
	reports := sink.Drain(0)
	require.Len(t, reports, 1, "the trip must emit exactly one usage report")
	assert.Equal(t, "fallback", reports[0].Entries["reason"])
	require.Equal(t, 0, agents.resets, "a trip must not touch agent QUIC state; only a recovered reprobe does")

	// Advance the probe: two failed attempts (still on fallback, no state touched),
	// then a successful one.
	calls := 0
	probe := func(context.Context) (*quic.Conn, string, error) {
		calls++
		if calls < 3 {
			return nil, "", errors.New("still unreachable")
		}
		return nil, "198.51.100.9:7778", nil
	}
	require.False(t, s.attemptReprobe(ctx, probe))
	require.False(t, s.attemptReprobe(ctx, probe))
	transport, _ = s.TransportStatus()
	require.Equal(t, TransportGRPCFallback, transport, "a failed reprobe attempt must not touch transport status")
	require.Empty(t, sink.Drain(0), "a failed reprobe attempt must not emit a usage report")
	require.Equal(t, 0, agents.resets)

	require.True(t, s.attemptReprobe(ctx, probe))

	transport, endpoint := s.TransportStatus()
	assert.Equal(t, TransportQUIC, transport, "a successful reprobe must swap the provider back to quic")
	assert.Equal(t, "198.51.100.9:7778", endpoint)
	assert.NotSame(t, trippedProvider, s.quicTunnelProvider.Load(),
		"reprobe must install a fresh provider, not resurrect the tripped one")
	assert.Equal(t, 1, agents.resets, "a successful reprobe must reset agent QUIC state exactly once")

	reports = sink.Drain(0)
	require.Len(t, reports, 1, "the recovery must emit exactly one usage report")
	assert.Equal(t, "quic", reports[0].Entries["transport"])
	assert.Equal(t, "reprobe", reports[0].Entries["reason"])

	// The freshly-installed provider has its own onFallback closure (from
	// s.onQuicFallback(ctx) inside activateQuicTunnel), independently CAS-guarded, so a
	// later trip of *this* provider fires the callback again -- "exactly once" means
	// once per transition, not once ever. newQuicFallbackProvider's own tests already
	// cover that a single provider only ever fires its callback once; what matters here
	// is that recovery installed a distinct provider capable of tripping on its own.
	recovered := s.quicTunnelProvider.Load()
	assert.NotNil(t, recovered.onFallback, "the recovered provider must carry its own onFallback callback")
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

// --- TLS session resumption ------------------------------------------------------------

// testResumptionCA is a minimal self-signed CA plus leaf-minting helpers, for tests that
// need a real (verified, not InsecureSkipVerify) TLS chain: quicTLSConfig always builds
// RootCAs from ep.CaPem, so a genuine dial requires a genuine chain to verify against.
type testResumptionCA struct {
	cert *x509.Certificate
	key  ed25519.PrivateKey
	pem  []byte
}

func newTestResumptionCA(t *testing.T) *testResumptionCA {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "resumption-test-ca"},
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
	return &testResumptionCA{cert: cert, key: priv, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (ca *testResumptionCA) mintServerCert(t *testing.T, sni string) tls.Certificate {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: sni},
		DNSNames:     []string{sni},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, pub, ca.key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

func (ca *testResumptionCA) mintClientCertPEM(t *testing.T, cn string) (certPEM, keyPEM []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
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

// TestSession_QuicTLSConfig_ResumesOnRedial is the unit-level proof required by the
// session-resumption plan: dialing the same peer twice through the session's own
// quicTLSConfig, sharing its quicSessionCache, resumes the second time and the resumed
// connection's streams work.
func TestSession_QuicTLSConfig_ResumesOnRedial(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ca := newTestResumptionCA(t)
	const sni = "quic-resume-test.example"
	serverCert := ca.mintServerCert(t, sni)
	clientCertPEM, clientKeyPEM := ca.mintClientCertPEM(t, "test-session")

	serverTLSConf := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		NextProtos:   []string{"quic-resume-test"},
	}
	ln, err := quic.ListenAddr("127.0.0.1:0", serverTLSConf, nil)
	require.NoError(t, err)
	defer ln.Close()

	streamCh := make(chan *quic.Stream, 2)
	go func() {
		for {
			conn, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			go func() {
				qs, err := conn.AcceptStream(ctx)
				if err != nil {
					return
				}
				streamCh <- qs
			}()
		}
	}()

	cache := &notifyingSessionCache{ClientSessionCache: tls.NewLRUClientSessionCache(16), put: make(chan struct{}, 1)}
	s := &session{quicSessionCache: cache}
	ep := &rpc.QuicTunnelEndpoint{
		CaPem:         ca.pem,
		ClientCertPem: clientCertPEM,
		ClientKeyPem:  clientKeyPEM,
		ServerName:    sni,
		Alpn:          "quic-resume-test",
	}

	dial := func() *quic.Conn {
		tlsConf, err := s.quicTLSConfig(ep)
		require.NoError(t, err)
		conn, err := quic.DialAddr(ctx, ln.Addr().String(), tlsConf, nil)
		require.NoError(t, err)
		return conn
	}

	conn1 := dial()
	require.False(t, conn1.ConnectionState().TLS.DidResume, "the first dial has no ticket to resume")
	qs1, err := conn1.OpenStreamSync(ctx)
	require.NoError(t, err)
	// A QUIC stream is only observed by the peer's AcceptStream once data actually flows
	// on it.
	_, err = qs1.Write([]byte("ping"))
	require.NoError(t, err)
	select {
	case <-streamCh:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the first stream to be accepted")
	}

	select {
	case <-cache.put:
	case <-ctx.Done():
		t.Fatal("timed out waiting for a session ticket to be cached")
	}
	require.NoError(t, conn1.CloseWithError(0, ""))

	conn2 := dial()
	defer func() { _ = conn2.CloseWithError(0, "") }()
	require.True(t, conn2.ConnectionState().TLS.DidResume, "re-dial with the same session cache must resume")

	qs2, err := conn2.OpenStreamSync(ctx)
	require.NoError(t, err)
	defer func() { _ = qs2.Close() }()
	_, err = qs2.Write([]byte("ping"))
	require.NoError(t, err)
	select {
	case <-streamCh:
	case <-ctx.Done():
		t.Fatal("timed out waiting for the resumed connection's stream to be accepted")
	}
}

// TestSession_QuicTLSConfig_NoSessionCacheNeverResumes documents the fallback shape a
// bare session{} (quicSessionCache == nil) leaves quicTLSConfig in: dialing is unaffected,
// resumption is simply never attempted, matching a plain tls.Config with no
// ClientSessionCache set.
func TestSession_QuicTLSConfig_NoSessionCacheNeverResumes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ca := newTestResumptionCA(t)
	const sni = "quic-no-resume-test.example"
	serverCert := ca.mintServerCert(t, sni)
	clientCertPEM, clientKeyPEM := ca.mintClientCertPEM(t, "test-session")

	serverTLSConf := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		NextProtos:   []string{"quic-no-resume-test"},
	}
	ln, err := quic.ListenAddr("127.0.0.1:0", serverTLSConf, nil)
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			go func() { _ = conn.CloseWithError(0, "") }()
		}
	}()

	s := &session{}
	ep := &rpc.QuicTunnelEndpoint{
		CaPem:         ca.pem,
		ClientCertPem: clientCertPEM,
		ClientKeyPem:  clientKeyPEM,
		ServerName:    sni,
		Alpn:          "quic-no-resume-test",
	}

	tlsConf, err := s.quicTLSConfig(ep)
	require.NoError(t, err)
	require.Nil(t, tlsConf.ClientSessionCache)

	conn, err := quic.DialAddr(ctx, ln.Addr().String(), tlsConf, nil)
	require.NoError(t, err)
	defer func() { _ = conn.CloseWithError(0, "") }()
	require.False(t, conn.ConnectionState().TLS.DidResume)
}

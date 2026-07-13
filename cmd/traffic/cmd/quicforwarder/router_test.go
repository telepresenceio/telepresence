package quicforwarder

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
)

// buildLongHeader constructs a structurally valid (but unencrypted -- fine for tests
// that never reach CryptoAccumulator.Feed, since the byte cap is checked first) QUIC v1
// long-header Initial packet, mirroring pkg/quicfwd's own test helper of the same
// shape.
func buildLongHeader(t *testing.T, dcid, scid, token, payload []byte) []byte {
	t.Helper()
	b := []byte{0xc0} // long header, fixed bit, Initial type (00), PN length bits 0
	var verBuf [4]byte
	binary.BigEndian.PutUint32(verBuf[:], quicfwd.Version1)
	b = append(b, verBuf[:]...)
	b = append(b, byte(len(dcid)))
	b = append(b, dcid...)
	b = append(b, byte(len(scid)))
	b = append(b, scid...)
	b = quicvarint.Append(b, uint64(len(token)))
	b = append(b, token...)
	b = quicvarint.Append(b, uint64(len(payload)))
	b = append(b, payload...)
	return b
}

// --- fakes -------------------------------------------------------------------------

type fakeAllowlist struct {
	mu         sync.Mutex
	ready      bool
	allowed    map[netip.Addr]bool
	manager    netip.Addr
	hasManager bool
}

func newFakeAllowlist() *fakeAllowlist {
	return &fakeAllowlist{ready: true, allowed: map[netip.Addr]bool{}}
}

func (f *fakeAllowlist) Ready() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func (f *fakeAllowlist) Contains(ip netip.Addr) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allowed[ip]
}

func (f *fakeAllowlist) ManagerAddr() (netip.Addr, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.manager, f.hasManager
}

func (f *fakeAllowlist) allow(ip netip.Addr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowed[ip] = true
}

func (f *fakeAllowlist) setManager(ip netip.Addr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowed[ip] = true
	f.manager = ip
	f.hasManager = true
}

type createCall struct {
	src       netip.AddrPort
	backend   netip.Addr
	datagrams [][]byte
}

type fakeSink struct {
	mu      sync.Mutex
	flows   map[netip.AddrPort]bool
	creates []createCall
	fwd     map[netip.AddrPort][][]byte
}

func newFakeSink() *fakeSink {
	return &fakeSink{flows: map[netip.AddrPort]bool{}, fwd: map[netip.AddrPort][][]byte{}}
}

func (f *fakeSink) Forward(_ context.Context, src netip.AddrPort, datagram []byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.flows[src] {
		return false
	}
	f.fwd[src] = append(f.fwd[src], datagram)
	return true
}

func (f *fakeSink) CreateAndForward(_ context.Context, src netip.AddrPort, backend netip.Addr, datagrams [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flows[src] = true
	cp := make([][]byte, len(datagrams))
	copy(cp, datagrams)
	f.creates = append(f.creates, createCall{src: src, backend: backend, datagrams: cp})
}

func (f *fakeSink) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates)
}

func (f *fakeSink) lastCreate() createCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates[len(f.creates)-1]
}

func someSrc() netip.AddrPort {
	return netip.MustParseAddrPort("192.0.2.1:12345")
}

// --- (a) existing flow ---------------------------------------------------------------

func TestRoute_ExistingFlowForwardsRegardlessOfContent(t *testing.T) {
	allowlist := newFakeAllowlist()
	sink := newFakeSink()
	src := someSrc()
	sink.flows[src] = true

	r := NewRouter(allowlist, sink, newMetrics())
	garbage := []byte{0xff, 0xff, 0xff}
	r.Route(context.Background(), src, garbage)

	assert.Equal(t, [][]byte{garbage}, sink.fwd[src])
	assert.Equal(t, int64(1), r.metrics.forwarded.Load())
	assert.Zero(t, sink.createCount())
}

// --- (b) short header CID routing -----------------------------------------------------

func TestRoute_ShortHeaderCIDHit(t *testing.T) {
	backendIP := netip.MustParseAddr("10.1.2.3")
	cid, err := quicfwd.EncodeCID(backendIP)
	require.NoError(t, err)

	allowlist := newFakeAllowlist()
	allowlist.allow(backendIP)
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	datagram := append([]byte{0x40}, cid...)
	datagram = append(datagram, []byte("1-rtt-payload")...)

	src := someSrc()
	r.Route(context.Background(), src, datagram)

	require.Equal(t, 1, sink.createCount())
	call := sink.lastCreate()
	assert.Equal(t, backendIP, call.backend)
	assert.Equal(t, [][]byte{datagram}, call.datagrams)
	assert.Equal(t, int64(1), r.metrics.forwarded.Load())
}

func TestRoute_ShortHeaderCIDAllowlistMiss(t *testing.T) {
	backendIP := netip.MustParseAddr("10.1.2.3")
	cid, err := quicfwd.EncodeCID(backendIP)
	require.NoError(t, err)

	allowlist := newFakeAllowlist() // backendIP deliberately not allowed
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	datagram := append([]byte{0x40}, cid...)
	r.Route(context.Background(), someSrc(), datagram)

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(1), r.metrics.dropped[dropAllowlistMiss].Load())
}

func TestRoute_ShortHeaderUndecodableCID(t *testing.T) {
	allowlist := newFakeAllowlist()
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	// Fixed-length but wrong magic byte: DecodeCID must reject it.
	datagram := append([]byte{0x40}, make([]byte, quicfwd.CIDLen)...)
	r.Route(context.Background(), someSrc(), datagram)

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(1), r.metrics.dropped[dropAllowlistMiss].Load())
}

// --- (e) unknown version / garbage -----------------------------------------------------

func TestRoute_UnknownVersionDropped(t *testing.T) {
	allowlist := newFakeAllowlist()
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	// Long header, version 0xdeadbeef, minimal DCID/SCID.
	datagram := []byte{0xc0, 0xde, 0xad, 0xbe, 0xef, 0x00, 0x00}
	r.Route(context.Background(), someSrc(), datagram)

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(1), r.metrics.dropped[dropUnknownVersion].Load())
}

func TestRoute_VersionNegotiationDropped(t *testing.T) {
	allowlist := newFakeAllowlist()
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	// Long header, version 0 (version negotiation).
	datagram := []byte{0xc0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	r.Route(context.Background(), someSrc(), datagram)

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(1), r.metrics.dropped[dropUnknownVersion].Load())
}

func TestRoute_GarbageDropped(t *testing.T) {
	allowlist := newFakeAllowlist()
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	r.Route(context.Background(), someSrc(), nil)
	r.Route(context.Background(), someSrc(), []byte{0x40}) // short header, too short for a CID

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(2), r.metrics.dropped[dropGarbage].Load())
}

// --- not-ready gate --------------------------------------------------------------------

func TestRoute_NotReadyDropsEverything(t *testing.T) {
	allowlist := newFakeAllowlist()
	allowlist.ready = false
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	r.Route(context.Background(), someSrc(), []byte{0x40, 1, 2, 3})
	r.Route(context.Background(), someSrc(), []byte{0xc0, 0, 0, 0, 1})

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(2), r.metrics.dropped[dropNotReady].Load())
}

// --- (c) Initial-packet handshake accumulation, using real quic-go client output -------

// captureInitialDatagrams dials a real quic-go client (the module's exact quic-go
// version) at a UDP socket that never answers, and returns every datagram it sent
// before giving up, exactly as pkg/quicfwd's capture_test.go does. The bloated ALPN
// list forces a multi-packet ClientHello split deterministically, regardless of any
// particular Go toolchain's default TLS configuration.
func captureInitialDatagrams(t *testing.T, sni string) [][]byte {
	t.Helper()

	serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer serverConn.Close()

	var packets [][]byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			n, _, err := serverConn.ReadFrom(buf)
			if err != nil {
				return
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			packets = append(packets, pkt)
		}
	}()

	alpn := make([]string, 64)
	for i := range alpn {
		alpn[i] = "quic-forwarder-test-alpn-filler-protocol"
	}
	tlsConf := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
		NextProtos:         alpn,
	}
	cfg := &quic.Config{
		Versions:             []quic.Version{quic.Version1},
		HandshakeIdleTimeout: 300 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, dialErr := quic.DialAddr(ctx, serverConn.LocalAddr().String(), tlsConf, cfg)
	require.Error(t, dialErr) // the fake "server" never answers; a handshake timeout is expected

	require.NoError(t, serverConn.SetReadDeadline(time.Now()))
	<-done
	require.NotEmpty(t, packets)
	return packets
}

func TestRoute_HandshakeAccumulation_RealMultiPacketSplit_BufferedFlushOrder(t *testing.T) {
	packets := captureInitialDatagrams(t, quicfwd.ManagerSNI)

	managerIP := netip.MustParseAddr("10.9.9.9")
	allowlist := newFakeAllowlist()
	allowlist.setManager(managerIP)
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	// The fake "server" behind captureInitialDatagrams never answers, so the
	// real quic-go client keeps retransmitting its Initial packets (PTO) for
	// the whole dial timeout; there is no guarantee the very last captured
	// datagram is the one whose arrival completes the reassembly (a
	// retransmission can arrive after completion just as well as before it).
	// Feed packets one at a time and stop at the first one that resolves --
	// that is the arrival-ordered prefix the handshake cache must have
	// buffered and flushed.
	src := someSrc()
	ctx := context.Background()
	resolvedAt := -1
	for i, pkt := range packets {
		r.Route(ctx, src, pkt)
		if sink.createCount() > 0 {
			resolvedAt = i
			break
		}
	}
	require.NotEqual(t, -1, resolvedAt, "SNI must resolve from some prefix of the captured packets")

	require.Equal(t, 1, sink.createCount())
	call := sink.lastCreate()
	assert.Equal(t, managerIP, call.backend)
	assert.Equal(t, packets[:resolvedAt+1], call.datagrams, "buffered datagrams must flush in arrival order")
	assert.Equal(t, int64(resolvedAt+1), r.metrics.forwarded.Load())

	// Once the flow exists (CreateAndForward already registered it in the
	// fake), a further datagram from the same source is forwarded via the
	// existing flow, not reclassified.
	r.Route(ctx, src, []byte("post-handshake-1-rtt-would-go-here"))
	assert.Equal(t, [][]byte{[]byte("post-handshake-1-rtt-would-go-here")}, sink.fwd[src])
}

func TestRoute_HandshakeAgentSNIUnresolvable(t *testing.T) {
	packets := captureInitialDatagrams(t, quicfwd.AgentSNI("some-pod-uid"))

	allowlist := newFakeAllowlist() // no manager, no agent backends -- phase 5
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	src := someSrc()
	ctx := context.Background()
	for _, pkt := range packets {
		r.Route(ctx, src, pkt)
	}

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(1), r.metrics.dropped[dropUnresolvedSNI].Load())
}

func TestRoute_HandshakeUnknownSNIUnresolvable(t *testing.T) {
	packets := captureInitialDatagrams(t, "not-a-recognized-name.example")

	allowlist := newFakeAllowlist()
	allowlist.setManager(netip.MustParseAddr("10.9.9.9"))
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	src := someSrc()
	ctx := context.Background()
	for _, pkt := range packets {
		r.Route(ctx, src, pkt)
	}

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(1), r.metrics.dropped[dropUnresolvedSNI].Load())
}

func TestRoute_HandshakeManagerSNIUnresolvableWithoutAllowlistedManager(t *testing.T) {
	packets := captureInitialDatagrams(t, quicfwd.ManagerSNI)

	allowlist := newFakeAllowlist() // no manager allowlisted at all
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	src := someSrc()
	ctx := context.Background()
	for _, pkt := range packets {
		r.Route(ctx, src, pkt)
	}

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(1), r.metrics.dropped[dropUnresolvedSNI].Load())
}

// --- handshake cache caps and TTL --------------------------------------------------------

func TestRoute_HandshakeCacheCapExceeded(t *testing.T) {
	packets := captureInitialDatagrams(t, quicfwd.ManagerSNI)
	require.GreaterOrEqual(t, len(packets), 2, "test requires a real multi-packet split")

	allowlist := newFakeAllowlist()
	allowlist.setManager(netip.MustParseAddr("10.9.9.9"))
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	src := someSrc()
	ctx := context.Background()
	// Feed the first (incomplete) packet repeatedly: it never resolves on its
	// own, so this simulates a stalled handshake attempt that keeps sending
	// (or retransmitting) Initial datagrams without ever completing.
	for i := 0; i < handshakeMaxDatagrams+1; i++ {
		r.Route(ctx, src, packets[0])
	}

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(1), r.metrics.dropped[dropCapExceeded].Load())

	// The entry was evicted by the cap; even feeding the real completing
	// packet now starts a brand new (empty) attempt rather than resolving.
	r.Route(ctx, src, packets[len(packets)-1])
	assert.Zero(t, sink.createCount())
}

func TestRoute_HandshakeCacheByteCapExceeded(t *testing.T) {
	allowlist := newFakeAllowlist()
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	// A single datagram whose payload alone pushes the entry over the byte
	// cap: the cap is checked before CryptoAccumulator.Feed is even called, so
	// this packet's content never needs to be real, decryptable Initial
	// bytes.
	dcid := []byte{1, 2, 3, 4}
	scid := []byte{5, 6}
	payload := make([]byte, handshakeMaxBytes+1)
	pkt := buildLongHeader(t, dcid, scid, nil, payload)

	src := someSrc()
	ctx := context.Background()
	r.Route(ctx, src, pkt)

	assert.Zero(t, sink.createCount())
	assert.Equal(t, int64(1), r.metrics.dropped[dropCapExceeded].Load())
}

func TestRoute_HandshakeTTLExpiry(t *testing.T) {
	packets := captureInitialDatagrams(t, quicfwd.ManagerSNI)
	require.GreaterOrEqual(t, len(packets), 2, "test requires a real multi-packet split")

	allowlist := newFakeAllowlist()
	allowlist.setManager(netip.MustParseAddr("10.9.9.9"))
	sink := newFakeSink()
	r := NewRouter(allowlist, sink, newMetrics())

	now := time.Now()
	r.clock = func() time.Time { return now }

	src := someSrc()
	ctx := context.Background()
	r.Route(ctx, src, packets[0])
	assert.Zero(t, sink.createCount())

	// Advance the fake clock past the TTL and sweep.
	now = now.Add(handshakeTTL + time.Millisecond)
	r.Sweep(ctx)
	assert.Equal(t, int64(1), r.metrics.dropped[dropTTLExpired].Load())

	// The completing packet now starts a fresh (incomplete) attempt instead
	// of resolving the expired one.
	r.Route(ctx, src, packets[len(packets)-1])
	assert.Zero(t, sink.createCount())
}

package quicforwarder

import (
	"context"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
)

const (
	// handshakeMaxDatagrams and handshakeMaxBytes bound how much a single
	// handshake-cache entry (one connection attempt's buffered datagrams,
	// keyed by source address + client DCID) will hold before it is dropped
	// unresolved. Per "The forwarder" section of
	// docs/reference/quic-transport-architecture.md this keeps the cache small and
	// ephemeral -- an attacker cannot make it grow arbitrarily by trickling
	// Initial-looking datagrams that never complete a handshake.
	handshakeMaxDatagrams = 8
	handshakeMaxBytes     = 16 * 1024

	// handshakeTTL bounds how long a handshake-cache entry is kept waiting for
	// its SNI to resolve before it is dropped, per the design's "seconds-scale
	// TTL".
	handshakeTTL = 3 * time.Second

	// dropLogRate limits how often a non-dropNotReady drop reason is logged;
	// the counters in metrics.go still count every drop regardless.
	dropLogEventsPerSecond = 5
	dropLogBurst           = 5
)

// backendPicker is the routing engine's view of the live backend allowlist. The real
// implementation is *Allowlist (allowlist.go); tests inject a fake, which is the whole
// reason Router's routing logic lives in this file separately from any real socket or
// gRPC code.
type backendPicker interface {
	// Ready reports whether at least one allowlist snapshot has ever been
	// received. Before that, every datagram is dropped.
	Ready() bool
	// Backend resolves ip to its QUIC port if it is a currently live,
	// allowlisted backend (manager or agent).
	Backend(ip netip.Addr) (port uint16, ok bool)
	// ManagerBackend returns any one currently allowlisted manager backend's
	// IP and port. ok is false when none is allowlisted.
	ManagerBackend() (ip netip.Addr, port uint16, ok bool)
	// AgentBackend resolves podUID (an AgentSNI's pod UID) to its currently
	// live, allowlisted IP and port. ok is false when no agent with that pod
	// UID is allowlisted.
	AgentBackend(podUID string) (ip netip.Addr, port uint16, ok bool)
}

// flowSink is the routing engine's view of the flow table. The real implementation is
// *flowTable (flow.go); tests inject a fake that records calls instead of opening real
// sockets.
type flowSink interface {
	// Forward writes datagram to the existing flow for src, if any, and
	// reports whether one existed.
	Forward(ctx context.Context, src netip.AddrPort, datagram []byte) bool
	// CreateAndForward creates a new flow from src to backend:port and writes
	// each of datagrams to it, in order.
	CreateAndForward(ctx context.Context, src netip.AddrPort, backend netip.Addr, port uint16, datagrams [][]byte)
}

// handshakeKey identifies one in-progress connection attempt: a client's source address
// together with the client-chosen Destination Connection ID it used on its first
// Initial packet (which stays constant across every Initial packet of that attempt).
type handshakeKey struct {
	src  netip.AddrPort
	dcid string
}

// handshakeEntry buffers one connection attempt's datagrams while its ClientHello is
// reassembled across Initial packets.
type handshakeEntry struct {
	acc        *quicfwd.CryptoAccumulator
	datagrams  [][]byte
	totalBytes int
	created    time.Time
}

// Router implements the forwarder's per-datagram routing decision (cases a-e of "The
// forwarder" in docs/reference/quic-transport-architecture.md). It is deliberately decoupled from
// any real socket: Route and Sweep take an explicit clock/context and delegate all
// observable effects to backendPicker and flowSink, so the whole decision tree is
// unit-testable with fakes.
type Router struct {
	allowlist backendPicker
	sink      flowSink
	metrics   *metrics
	clock     func() time.Time
	dropLimit *rate.Limiter

	mu              sync.Mutex
	handshakes      map[handshakeKey]*handshakeEntry
	notReadyLogOnce sync.Once
}

// NewRouter returns a Router that resolves backends via allowlist and forwards through
// sink, recording counters in m.
func NewRouter(allowlist backendPicker, sink flowSink, m *metrics) *Router {
	return &Router{
		allowlist:  allowlist,
		sink:       sink,
		metrics:    m,
		clock:      time.Now,
		dropLimit:  rate.NewLimiter(rate.Limit(dropLogEventsPerSecond), dropLogBurst),
		handshakes: make(map[handshakeKey]*handshakeEntry),
	}
}

// dropf counts a dropped datagram and, subject to rate limiting, logs it at debug.
// dropNotReady is special-cased to a single info-level log instead (per the design:
// "Until the first snapshot: drop everything (log once at info)"), since logging every
// datagram dropped for that reason at startup would otherwise dominate the log.
func (r *Router) dropf(ctx context.Context, reason dropReason, format string, args ...any) {
	r.metrics.addDrop(reason)
	if reason == dropNotReady {
		r.notReadyLogOnce.Do(func() {
			clog.Infof(ctx, "quic-forwarder: no backend allowlist yet; dropping all QUIC traffic until the first snapshot arrives")
		})
		return
	}
	if r.dropLimit.Allow() {
		clog.Debugf(ctx, "quic-forwarder: drop[%s]: "+format, append([]any{reason}, args...)...)
	}
}

// Route makes the routing decision for one datagram received from src on the
// forwarder's front socket, and forwards it (immediately, or once buffered and later
// flushed) or drops it. It never blocks on anything but the flowSink/backendPicker
// calls it makes.
func (r *Router) Route(ctx context.Context, src netip.AddrPort, datagram []byte) {
	if !r.allowlist.Ready() {
		r.dropf(ctx, dropNotReady, "")
		return
	}

	// (a) An existing flow for this source address always wins, regardless of
	// what the datagram contains: once routed, a connection's packets are
	// never re-classified.
	if r.sink.Forward(ctx, src, datagram) {
		r.metrics.addForwarded(1)
		return
	}

	packets, err := quicfwd.SplitCoalesced(datagram)
	if err != nil || len(packets) == 0 {
		r.dropf(ctx, dropGarbage, "malformed datagram from %s: %v", src, err)
		return
	}
	first := packets[0]
	info, err := quicfwd.ParsePacket(first)
	if err != nil {
		r.dropf(ctx, dropGarbage, "unparseable packet from %s: %v", src, err)
		return
	}

	switch info.Kind {
	case quicfwd.KindShortHeader:
		// (b) Short header: route by server-issued CID.
		r.routeByCID(ctx, src, info.DCID, datagram)
	case quicfwd.KindLongHeader:
		if info.Version != quicfwd.Version1 {
			r.dropf(ctx, dropUnknownVersion, "unsupported QUIC version 0x%08x from %s", info.Version, src)
			return
		}
		if info.Type == quicfwd.TypeInitial {
			// (c) Long-header Initial: handshake accumulator.
			r.routeInitial(ctx, src, info.DCID, first, datagram)
			return
		}
		// (d) Long-header non-Initial without a flow: try CID decode.
		r.routeByCID(ctx, src, info.DCID, datagram)
	default:
		// (e) KindVersionNegotiation, or anything else ParsePacket might one
		// day add: unroutable.
		r.dropf(ctx, dropUnknownVersion, "unroutable %s packet from %s", info.Kind, src)
	}
}

// routeByCID implements cases (b) and (d): decode dcid to a pod IP and, if it is a
// live, allowlisted backend, create a flow and forward datagram to it.
func (r *Router) routeByCID(ctx context.Context, src netip.AddrPort, dcid []byte, datagram []byte) {
	ip, ok := quicfwd.DecodeCID(dcid)
	if !ok {
		r.dropf(ctx, dropAllowlistMiss, "CID does not resolve to an allowlisted backend (src %s)", src)
		return
	}
	port, ok := r.allowlist.Backend(ip)
	if !ok {
		r.dropf(ctx, dropAllowlistMiss, "CID does not resolve to an allowlisted backend (src %s)", src)
		return
	}
	r.sink.CreateAndForward(ctx, src, ip, port, [][]byte{datagram})
	r.metrics.addForwarded(1)
}

// routeInitial implements case (c): buffer datagram in the handshake cache entry for
// (src, dcid), feed first to that entry's CryptoAccumulator, and -- once the SNI
// resolves to an allowlisted backend -- create the flow and flush every datagram
// buffered so far, in arrival order.
func (r *Router) routeInitial(ctx context.Context, src netip.AddrPort, dcid []byte, first, datagram []byte) {
	key := handshakeKey{src: src, dcid: string(dcid)}

	r.mu.Lock()
	entry, ok := r.handshakes[key]
	if !ok {
		entry = &handshakeEntry{acc: quicfwd.NewCryptoAccumulator(), created: r.clock()}
		r.handshakes[key] = entry
	}
	entry.datagrams = append(entry.datagrams, datagram)
	entry.totalBytes += len(datagram)
	exceeded := len(entry.datagrams) > handshakeMaxDatagrams || entry.totalBytes > handshakeMaxBytes
	if exceeded {
		delete(r.handshakes, key)
	}
	r.mu.Unlock()

	if exceeded {
		r.dropf(ctx, dropCapExceeded, "handshake cache cap exceeded for %s; dropping buffered datagrams", src)
		return
	}

	sni, sniOK, err := entry.acc.Feed(first)
	if err != nil {
		r.mu.Lock()
		delete(r.handshakes, key)
		r.mu.Unlock()
		r.dropf(ctx, dropGarbage, "malformed Initial packet from %s: %v", src, err)
		return
	}
	if !sniOK {
		// Still incomplete; wait for the next Initial packet of this attempt
		// (or eviction by Sweep on cap/TTL).
		return
	}

	r.mu.Lock()
	buffered := entry.datagrams
	delete(r.handshakes, key)
	r.mu.Unlock()

	backendKind, podUID := quicfwd.ParseSNI(sni)
	var (
		backendIP   netip.Addr
		backendPort uint16
		resolved    bool
	)
	switch backendKind {
	case quicfwd.BackendManager:
		backendIP, backendPort, resolved = r.allowlist.ManagerBackend()
	case quicfwd.BackendAgent:
		backendIP, backendPort, resolved = r.allowlist.AgentBackend(podUID)
	default:
		resolved = false
	}
	if !resolved {
		r.dropf(ctx, dropUnresolvedSNI, "SNI %q (kind %s) did not resolve to an allowlisted backend (src %s)", sni, backendKind, src)
		return
	}

	r.sink.CreateAndForward(ctx, src, backendIP, backendPort, buffered)
	r.metrics.addForwarded(int64(len(buffered)))
}

// Sweep evicts handshake-cache entries older than handshakeTTL that never resolved.
// Intended to be called periodically by the forwarder's run loop.
func (r *Router) Sweep(ctx context.Context) {
	now := r.clock()
	r.mu.Lock()
	var expired []handshakeKey
	for k, e := range r.handshakes {
		if now.Sub(e.created) > handshakeTTL {
			expired = append(expired, k)
		}
	}
	for _, k := range expired {
		delete(r.handshakes, k)
	}
	r.mu.Unlock()

	for range expired {
		r.dropf(ctx, dropTTLExpired, "handshake cache entry expired without resolving")
	}
}

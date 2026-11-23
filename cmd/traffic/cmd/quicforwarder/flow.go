package quicforwarder

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"

	"github.com/telepresenceio/clog"
)

// flowEntry is one established client<->backend flow: a connected UDP socket to the
// backend (plus its batch-I/O wrapper), and the last time either direction saw traffic
// (used by sweepIdle).
type flowEntry struct {
	conn       *net.UDPConn
	pc         *ipv4.PacketConn
	gro        bool         // UDP_GRO enabled on conn; see enableGRO.
	lastActive atomic.Int64 // UnixNano
	heard      atomic.Bool  // the backend has sent at least one datagram back
	ipKey      netip.Addr   // source-IP key counted against the per-IP cap
	created    int64        // UnixNano
}

func (e *flowEntry) touch() {
	e.lastActive.Store(time.Now().UnixNano())
}

// flowTable is the real, socket-owning implementation of flowSink: one goroutine per
// established flow pumps backend->client traffic onto the forwarder's single front
// socket, addressed back to the client's source address. This is the "soft state" the
// design describes: a forwarder restart loses the table, but clients simply re-appear
// via CID or SNI routing and QUIC's path validation handles the new return path.
//
// Keying flows by the client's source address alone encodes a system-wide invariant:
// a client UDP socket carries at most one QUIC connection (every Telepresence dial
// creates a socket of its own), so one source address maps to exactly one backend.
// See "The forwarder" in docs/reference/quic-transport-architecture.md.
type flowTable struct {
	frontPC *ipv4.PacketConn
	metrics *metrics

	batchPools [2]sync.Pool // indexed by whether GRO is enabled

	// Limits, settable by tests before use.
	maxFlows      int
	maxFlowsPerIP int
	halfOpenAfter time.Duration

	mu    sync.RWMutex
	flows map[netip.AddrPort]*flowEntry
	perIP map[netip.Addr]int
	// reserved counts flows in the table plus those being created.
	reserved int
	// totalCapLogged and ipCapLogged are true from the first drop at a cap until
	// a flow is released from a table or source at that cap; they guard the
	// once-per-episode log line.
	totalCapLogged bool
	ipCapLogged    bool
}

const (
	defaultMaxFlows      = 4096
	defaultMaxFlowsPerIP = 64
)

// ipKey maps a source address to its per-IP cap bucket: the unmapped address for
// IPv4 and the /64 prefix for IPv6.
func ipKey(a netip.Addr) netip.Addr {
	a = a.Unmap()
	if a.Is6() {
		return netip.PrefixFrom(a, 64).Masked().Addr()
	}
	return a
}

// batchBuffers is the working memory of one pump read/relay cycle.
type batchBuffers struct {
	rmsgs   []ipv4.Message
	wmsgs   []ipv4.Message
	scratch []byte
}

func (t *flowTable) getBatchBuffers(gro bool) *batchBuffers {
	i := 0
	if gro {
		i = 1
	}
	if b, ok := t.batchPools[i].Get().(*batchBuffers); ok {
		return b
	}
	return &batchBuffers{
		rmsgs:   newReadBatchMessages(batchSize, gro),
		wmsgs:   make([]ipv4.Message, 0, batchSize),
		scratch: make([]byte, maxDatagramSize),
	}
}

func (t *flowTable) putBatchBuffers(gro bool, b *batchBuffers) {
	i := 0
	if gro {
		i = 1
	}
	t.batchPools[i].Put(b)
}

// waitReadable blocks until conn has data to read or is closed. RawConn.Read discards any
// readiness seen before the call, so the callback itself checks for queued data.
func waitReadable(conn *net.UDPConn) error {
	rc, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	return rc.Read(readable)
}

func newFlowTable(frontPC *ipv4.PacketConn, m *metrics) *flowTable {
	return &flowTable{
		frontPC: frontPC,
		metrics: m,
		flows:   make(map[netip.AddrPort]*flowEntry),
		perIP:   make(map[netip.Addr]int),

		maxFlows:      defaultMaxFlows,
		maxFlowsPerIP: defaultMaxFlowsPerIP,
		halfOpenAfter: defaultHalfOpenExpiry,
	}
}

// Forward implements flowSink.
func (t *flowTable) Forward(ctx context.Context, src netip.AddrPort, datagram []byte) bool {
	t.mu.RLock()
	e, ok := t.flows[src]
	t.mu.RUnlock()
	if !ok {
		return false
	}
	e.touch()
	if _, err := e.conn.Write(datagram); err != nil {
		clog.Debugf(ctx, "quic-forwarder: write to backend for flow %s failed: %v", src, err)
	}
	return true
}

// lookup returns the established flow for src, if any, without touching lastActive or
// writing. Used by Forwarder.runIngress to decide, per datagram, whether it belongs to
// the fast batched path (an existing flow) or must go through Router.Route.
func (t *flowTable) lookup(src netip.AddrPort) (*flowEntry, bool) {
	t.mu.RLock()
	e, ok := t.flows[src]
	t.mu.RUnlock()
	return e, ok
}

// writeBatchToBackend writes every message in msgs -- an existing flow's consecutive
// datagrams, in arrival order -- to e's backend connection (GSO where the datagrams are
// uniformly sized and the kernel supports it, plain sendmmsg batching otherwise), and
// touches e's lastActive once. It always returns len(msgs): a write failure is logged
// and the rest of the group is silently dropped, the same silent-drop-on-error behavior
// Forward already applies per datagram. scratch is Forwarder.runIngress's reusable GSO
// assembly buffer.
func (t *flowTable) writeBatchToBackend(ctx context.Context, e *flowEntry, msgs []ipv4.Message, scratch []byte) int {
	e.touch()
	if err := writeMsgsBatch(e.pc, msgs, scratch); err != nil {
		clog.Debugf(ctx, "quic-forwarder: batch write to backend failed: %v", err)
	}
	return len(msgs)
}

// CreateAndForward implements flowSink. It returns the number of datagrams that were not
// dropped because the flow could not be created.
func (t *flowTable) CreateAndForward(ctx context.Context, src netip.AddrPort, backendIP netip.Addr, port uint16, datagrams [][]byte) int {
	backendAddr := netip.AddrPortFrom(backendIP, port)
	key := ipKey(src.Addr())
	if !t.reserve(ctx, src, key, len(datagrams)) {
		return 0
	}
	conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(backendAddr))
	if err != nil {
		t.release(key)
		clog.Debugf(ctx, "quic-forwarder: dial backend %s for flow %s failed: %v", backendAddr, src, err)
		return 0
	}
	raiseSocketBuffers(conn)
	gro := enableGRO(conn)

	e := &flowEntry{conn: conn, pc: ipv4.NewPacketConn(conn), gro: gro, ipKey: key, created: time.Now().UnixNano()}
	e.touch()

	t.mu.Lock()
	if existing, ok := t.flows[src]; ok {
		t.releaseLocked(key)
		// A flow for this source address was created concurrently (the main
		// ingress loop is single-threaded today, so this is only a defensive
		// path, not an expected race). Keep the existing one and use it
		// instead of leaking the redundant connection.
		t.mu.Unlock()
		_ = conn.Close()
		existing.touch()
		if err := writeBatchAll(existing.pc, msgsForDatagrams(datagrams)); err != nil {
			clog.Debugf(ctx, "quic-forwarder: write to backend for flow %s failed: %v", src, err)
		}
		return len(datagrams)
	}
	t.flows[src] = e
	t.mu.Unlock()

	clog.Debugf(ctx, "quic-forwarder: new flow %s -> %s", src, backendAddr)
	go t.pump(ctx, src, e)

	if err := writeBatchAll(e.pc, msgsForDatagrams(datagrams)); err != nil {
		clog.Debugf(ctx, "quic-forwarder: write to backend %s for flow %s failed: %v", backendAddr, src, err)
	}
	return len(datagrams)
}

// reserve counts a prospective flow for src against the caps. It reports false, and
// counts the n datagrams as dropped, when either cap is reached. An existing flow for src is not
// counted again.
func (t *flowTable) reserve(ctx context.Context, src netip.AddrPort, key netip.Addr, n int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.flows[src]; !ok {
		switch {
		case t.maxFlows > 0 && t.reserved >= t.maxFlows:
			t.metrics.addDrops(dropFlowCap, int64(n))
			if !t.totalCapLogged {
				t.totalCapLogged = true
				clog.Warnf(ctx, "quic-forwarder: flow limit %d reached; dropping datagrams that would create new flows", t.maxFlows)
			}
			return false
		case t.maxFlowsPerIP > 0 && t.perIP[key] >= t.maxFlowsPerIP:
			t.metrics.addDrops(dropFlowCap, int64(n))
			if !t.ipCapLogged {
				t.ipCapLogged = true
				clog.Warnf(ctx, "quic-forwarder: per-address flow limit %d reached for %s; dropping datagrams that would create new flows", t.maxFlowsPerIP, key)
			}
			return false
		}
	}
	t.reserved++
	t.perIP[key]++
	return true
}

func (t *flowTable) release(key netip.Addr) {
	t.mu.Lock()
	t.releaseLocked(key)
	t.mu.Unlock()
}

func (t *flowTable) releaseLocked(key netip.Addr) {
	if t.maxFlows > 0 && t.reserved >= t.maxFlows {
		t.totalCapLogged = false
	}
	if t.maxFlowsPerIP > 0 && t.perIP[key] >= t.maxFlowsPerIP {
		t.ipCapLogged = false
	}
	t.reserved--
	if t.perIP[key] <= 1 {
		delete(t.perIP, key)
	} else {
		t.perIP[key]--
	}
}

// pump reads backend->client traffic off e's backend socket (ReadBatch, i.e. recvmmsg
// on Linux) and relays it to src on the shared front socket with one WriteBatch per
// read (or, when the read's datagrams are uniformly sized and the kernel supports it,
// one GSO write), until the backend socket is closed (by sweepIdle or closeAll). When
// e.gro is set, a read message may be a UDP_GRO-coalesced super-datagram covering
// several of the backend's wire packets; splitGRO expands each one back into its own
// ipv4.Message before the write, so the GSO write path re-coalesces from individual
// QUIC packet boundaries rather than re-sending whatever the kernel happened to bundle
// together on receipt.
func (t *flowTable) pump(ctx context.Context, src netip.AddrPort, e *flowEntry) {
	addr := net.UDPAddrFromAddrPort(src)
	var wmsgs []ipv4.Message
	yield := func(data []byte) {
		wmsgs = append(wmsgs, ipv4.Message{Buffers: [][]byte{data}, Addr: addr})
	}

	for {
		// Buffers are held only while a batch is in flight, so idle and
		// half-open flows cost almost nothing.
		if err := waitReadable(e.conn); err != nil {
			return
		}
		b := t.getBatchBuffers(e.gro)
		n, err := e.pc.ReadBatch(b.rmsgs, 0)
		if err != nil {
			t.putBatchBuffers(e.gro, b)
			return
		}
		e.heard.Store(true)
		e.touch()
		wmsgs = b.wmsgs[:0]
		for i := range n {
			splitGRO(&b.rmsgs[i], yield)
		}
		if err := writeMsgsBatch(t.frontPC, wmsgs, b.scratch); err != nil {
			clog.Debugf(ctx, "quic-forwarder: write to client %s failed: %v", src, err)
		}
		b.wmsgs = wmsgs[:0]
		t.putBatchBuffers(e.gro, b)
	}
}

// sweepIdle closes and removes every flow that has seen no traffic in either
// direction for longer than idle, or, if the backend never answered, for longer than
// halfOpenAfter.
func (t *flowTable) sweepIdle(ctx context.Context, idle time.Duration) {
	now := time.Now()
	cutoff := now.Add(-idle).UnixNano()
	halfOpenCutoff := now.Add(-min(idle, t.halfOpenAfter)).UnixNano()
	t.mu.Lock()
	var closing []netip.AddrPort
	for src, e := range t.flows {
		// A half-open flow expires by age, whatever the client sends.
		if e.heard.Load() {
			if e.lastActive.Load() >= cutoff {
				continue
			}
		} else if e.created >= halfOpenCutoff && e.lastActive.Load() >= cutoff {
			continue
		}
		closing = append(closing, src)
	}
	entries := make([]*flowEntry, 0, len(closing))
	for _, src := range closing {
		e := t.flows[src]
		entries = append(entries, e)
		delete(t.flows, src)
		t.releaseLocked(e.ipKey)
	}
	t.mu.Unlock()

	for i, e := range entries {
		clog.Debugf(ctx, "quic-forwarder: closing idle flow %s", closing[i])
		_ = e.conn.Close()
	}
}

// closeAll closes every flow. Called once, from Serve's shutdown path.
func (t *flowTable) closeAll() {
	t.mu.Lock()
	entries := make([]*flowEntry, 0, len(t.flows))
	for _, e := range t.flows {
		entries = append(entries, e)
	}
	clear(t.flows)
	clear(t.perIP)
	t.reserved = 0
	t.totalCapLogged, t.ipCapLogged = false, false
	t.mu.Unlock()

	for _, e := range entries {
		_ = e.conn.Close()
	}
}

// count reports the number of currently established flows. Test-only convenience.
func (t *flowTable) count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.flows)
}

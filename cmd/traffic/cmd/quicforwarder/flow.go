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
	lastActive atomic.Int64 // UnixNano
}

func (e *flowEntry) touch() {
	e.lastActive.Store(time.Now().UnixNano())
}

// flowTable is the real, socket-owning implementation of flowSink: one goroutine per
// established flow pumps backend->client traffic onto the forwarder's single front
// socket, addressed back to the client's source address. This is the "soft state" the
// design describes: a forwarder restart loses the table, but clients simply re-appear
// via CID or SNI routing and QUIC's path validation handles the new return path.
type flowTable struct {
	frontPC *ipv4.PacketConn
	metrics *metrics

	mu    sync.RWMutex
	flows map[netip.AddrPort]*flowEntry
}

func newFlowTable(frontPC *ipv4.PacketConn, m *metrics) *flowTable {
	return &flowTable{
		frontPC: frontPC,
		metrics: m,
		flows:   make(map[netip.AddrPort]*flowEntry),
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

// CreateAndForward implements flowSink.
func (t *flowTable) CreateAndForward(ctx context.Context, src netip.AddrPort, backendIP netip.Addr, port uint16, datagrams [][]byte) {
	backendAddr := netip.AddrPortFrom(backendIP, port)
	conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(backendAddr))
	if err != nil {
		clog.Debugf(ctx, "quic-forwarder: dial backend %s for flow %s failed: %v", backendAddr, src, err)
		return
	}

	e := &flowEntry{conn: conn, pc: ipv4.NewPacketConn(conn)}
	e.touch()

	t.mu.Lock()
	if existing, ok := t.flows[src]; ok {
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
		return
	}
	t.flows[src] = e
	t.mu.Unlock()

	clog.Debugf(ctx, "quic-forwarder: new flow %s -> %s", src, backendAddr)
	go t.pump(ctx, src, e)

	if err := writeBatchAll(e.pc, msgsForDatagrams(datagrams)); err != nil {
		clog.Debugf(ctx, "quic-forwarder: write to backend %s for flow %s failed: %v", backendAddr, src, err)
	}
}

// pump reads backend->client traffic off e's backend socket (ReadBatch, i.e. recvmmsg
// on Linux) and relays it to src on the shared front socket with one WriteBatch per
// read (or, when the read's datagrams are uniformly sized and the kernel supports it,
// one GSO write), until the backend socket is closed (by sweepIdle or closeAll).
func (t *flowTable) pump(ctx context.Context, src netip.AddrPort, e *flowEntry) {
	rmsgs := newBatchMessages(batchSize)
	wmsgs := make([]ipv4.Message, batchSize)
	scratch := make([]byte, maxDatagramSize)
	addr := net.UDPAddrFromAddrPort(src)

	for {
		n, err := e.pc.ReadBatch(rmsgs, 0)
		if err != nil {
			return
		}
		e.touch()
		for i := range n {
			wmsgs[i].Buffers = [][]byte{rmsgs[i].Buffers[0][:rmsgs[i].N]}
			wmsgs[i].Addr = addr
		}
		if err := writeMsgsBatch(t.frontPC, wmsgs[:n], scratch); err != nil {
			clog.Debugf(ctx, "quic-forwarder: write to client %s failed: %v", src, err)
		}
	}
}

// sweepIdle closes and removes every flow that has seen no traffic in either
// direction for longer than idle.
func (t *flowTable) sweepIdle(ctx context.Context, idle time.Duration) {
	cutoff := time.Now().Add(-idle).UnixNano()
	t.mu.Lock()
	var closing []netip.AddrPort
	for src, e := range t.flows {
		if e.lastActive.Load() < cutoff {
			closing = append(closing, src)
		}
	}
	entries := make([]*flowEntry, 0, len(closing))
	for _, src := range closing {
		entries = append(entries, t.flows[src])
		delete(t.flows, src)
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

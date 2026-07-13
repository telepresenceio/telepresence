package quicforwarder

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/telepresenceio/clog"
)

// flowEntry is one established client<->backend flow: a connected UDP socket to the
// backend, and the last time either direction saw traffic (used by sweepIdle).
type flowEntry struct {
	conn       *net.UDPConn
	lastActive atomic.Int64 // UnixNano
}

func (e *flowEntry) touch() {
	e.lastActive.Store(time.Now().UnixNano())
}

// flowTable is the real, socket-owning implementation of flowSink: one goroutine per
// established flow pumps backend->client traffic onto the forwarder's single front
// socket, addressed back to the client's source address (WriteToUDPAddrPort). This is
// the "soft state" the design describes: a forwarder restart loses the table, but
// clients simply re-appear via CID or SNI routing and QUIC's path validation handles
// the new return path.
type flowTable struct {
	front   *net.UDPConn
	metrics *metrics

	mu    sync.RWMutex
	flows map[netip.AddrPort]*flowEntry
}

func newFlowTable(front *net.UDPConn, m *metrics) *flowTable {
	return &flowTable{
		front:   front,
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

// CreateAndForward implements flowSink.
func (t *flowTable) CreateAndForward(ctx context.Context, src netip.AddrPort, backendIP netip.Addr, port uint16, datagrams [][]byte) {
	backendAddr := netip.AddrPortFrom(backendIP, port)
	conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(backendAddr))
	if err != nil {
		clog.Debugf(ctx, "quic-forwarder: dial backend %s for flow %s failed: %v", backendAddr, src, err)
		return
	}

	e := &flowEntry{conn: conn}
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
		for _, dg := range datagrams {
			if _, err := existing.conn.Write(dg); err != nil {
				clog.Debugf(ctx, "quic-forwarder: write to backend for flow %s failed: %v", src, err)
			}
		}
		return
	}
	t.flows[src] = e
	t.mu.Unlock()

	clog.Debugf(ctx, "quic-forwarder: new flow %s -> %s", src, backendAddr)
	go t.pump(ctx, src, e)

	for _, dg := range datagrams {
		if _, err := conn.Write(dg); err != nil {
			clog.Debugf(ctx, "quic-forwarder: write to backend %s for flow %s failed: %v", backendAddr, src, err)
		}
	}
}

// pump reads backend->client traffic off e's backend socket and relays it to src on
// the shared front socket, until the backend socket is closed (by sweepIdle or
// closeAll).
func (t *flowTable) pump(ctx context.Context, src netip.AddrPort, e *flowEntry) {
	buf := make([]byte, 65535)
	for {
		n, err := e.conn.Read(buf)
		if err != nil {
			return
		}
		e.touch()
		if _, err := t.front.WriteToUDPAddrPort(buf[:n], src); err != nil {
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

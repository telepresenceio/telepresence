package quicforwarder

import (
	"context"
	"fmt"
	"net"
	"time"

	"golang.org/x/net/ipv4"

	"github.com/telepresenceio/clog"
)

const (
	// defaultIdleExpiry is how long an established flow may see no traffic in
	// either direction before it is closed, per the design's "Idle expiry 2
	// minutes (sweeper)".
	defaultIdleExpiry = 2 * time.Minute

	idleSweepInterval      = 15 * time.Second
	handshakeSweepInterval = 500 * time.Millisecond
	metricsLogInterval     = 30 * time.Second

	maxDatagramSize = 65535
)

// Forwarder is the runtime built on top of Router: it owns the single UDP socket
// clients dial (env.ListenPort), the flow table, and the periodic sweeps, and drives
// Router.Route with datagrams read off that socket.
type Forwarder struct {
	front   *net.UDPConn
	frontPC *ipv4.PacketConn
	env     *Env
	router  *Router
	flows   *flowTable
	metrics *metrics
}

// Listen binds the forwarder's front UDP socket on env.ListenPort (all interfaces) and
// wires up the routing engine against allowlist. It does not start serving; call
// Serve(ctx) for that.
func Listen(env *Env, allowlist *Allowlist) (*Forwarder, error) {
	front, err := net.ListenUDP("udp", &net.UDPAddr{Port: int(env.ListenPort)})
	if err != nil {
		return nil, fmt.Errorf("quic-forwarder: listen on UDP :%d: %w", env.ListenPort, err)
	}
	frontPC := ipv4.NewPacketConn(front)
	m := newMetrics()
	flows := newFlowTable(frontPC, m)
	router := NewRouter(allowlist, flows, m)
	return &Forwarder{
		front:   front,
		frontPC: frontPC,
		env:     env,
		router:  router,
		flows:   flows,
		metrics: m,
	}, nil
}

// Addr returns the forwarder's front socket's local address.
func (f *Forwarder) Addr() net.Addr {
	return f.front.LocalAddr()
}

// Serve runs the forwarder until ctx is done: it reads datagrams off the front socket
// and hands each to Router.Route, while periodic goroutines sweep idle flows, evict
// expired handshake-cache entries, and log counters. On ctx cancellation it closes the
// front socket and every established flow, waits for the sweep loop to exit, and
// returns nil -- per the design, this is unambiguous and immediate for every consumer:
// every QUIC connection through this forwarder simply stops working the moment it
// stops running.
func (f *Forwarder) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = f.front.Close()
	}()

	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		idleTicker := time.NewTicker(idleSweepInterval)
		defer idleTicker.Stop()
		handshakeTicker := time.NewTicker(handshakeSweepInterval)
		defer handshakeTicker.Stop()
		metricsTicker := time.NewTicker(metricsLogInterval)
		defer metricsTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-idleTicker.C:
				f.flows.sweepIdle(ctx, defaultIdleExpiry)
			case <-handshakeTicker.C:
				f.router.Sweep(ctx)
			case <-metricsTicker.C:
				f.metrics.LogSnapshot(ctx)
			}
		}
	}()

	f.runIngress(ctx)

	<-sweepDone
	f.flows.closeAll()
	return nil
}

// runIngress reads batches of datagrams off the front socket (ReadBatch, i.e.
// recvmmsg on Linux) and routes each one, in order. It is the single goroutine whose
// ordering guarantee the handshake cache relies on (concurrent Initial packets for one
// connection attempt are never processed out of order relative to each other), so it
// must stay single-threaded.
//
// A datagram whose source address already has an established flow bypasses
// Router.Route entirely -- Route's own case-(a) check would just confirm the same
// thing -- and instead joins a run of consecutive datagrams for that same flow. A run
// is flushed, with one WriteBatch to the flow's backend socket, whenever it ends: the
// destination flow changes, or the current datagram belongs to no established flow.
// That guarantees order is preserved both within a flow's run and relative to any
// interleaved datagram that still needs Router.Route (new-flow dial, handshake
// buffering, or drop), which stays exactly as before: unbatched, since it is cold
// relative to steady-state throughput.
func (f *Forwarder) runIngress(ctx context.Context) {
	msgs := newBatchMessages(batchSize)
	group := make([]ipv4.Message, 0, batchSize)
	scratch := make([]byte, maxDatagramSize)
	var groupEntry *flowEntry

	flush := func() {
		if groupEntry == nil {
			return
		}
		n := f.flows.writeBatchToBackend(ctx, groupEntry, group, scratch)
		f.metrics.addForwarded(int64(n))
		group = group[:0]
		groupEntry = nil
	}

	for {
		n, err := f.frontPC.ReadBatch(msgs, 0)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			clog.Debugf(ctx, "quic-forwarder: read error: %v", err)
			continue
		}
		for i := range n {
			m := &msgs[i]
			if m.N <= 0 {
				continue
			}
			ua, ok := m.Addr.(*net.UDPAddr)
			if !ok {
				continue
			}
			src := ua.AddrPort()
			data := m.Buffers[0][:m.N]

			if e, ok := f.flows.lookup(src); ok {
				if groupEntry != e {
					flush()
					groupEntry = e
				}
				group = append(group, ipv4.Message{Buffers: [][]byte{data}})
				continue
			}

			// Cold path: no established flow yet. Flush any pending run first
			// so writes stay in arrival order, then hand this one datagram to
			// Router.Route exactly as before -- including the durable copy,
			// since Route may retain it (the handshake cache) past this call,
			// well after this batch's buffers are reused.
			flush()
			datagram := make([]byte, len(data))
			copy(datagram, data)
			f.router.Route(ctx, src, datagram)
		}
		flush()
	}
}

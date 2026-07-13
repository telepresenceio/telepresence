package quicforwarder

import (
	"context"
	"fmt"
	"net"
	"time"

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
	m := newMetrics()
	flows := newFlowTable(front, env.BackendPort, m)
	router := NewRouter(allowlist, flows, m)
	return &Forwarder{
		front:   front,
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

	buf := make([]byte, maxDatagramSize)
	for {
		n, src, err := f.front.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			clog.Debugf(ctx, "quic-forwarder: read error: %v", err)
			continue
		}
		datagram := make([]byte, n)
		copy(datagram, buf[:n])
		f.router.Route(ctx, src, datagram)
	}

	<-sweepDone
	f.flows.closeAll()
	return nil
}

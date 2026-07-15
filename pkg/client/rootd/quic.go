package rootd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/usg"
)

// quicDialTimeout bounds the opportunistic probe/dial of the traffic-manager's QUIC
// tunnel endpoint. It exists so that a manager with no endpoint, or an endpoint behind
// a firewall that silently drops UDP, cannot delay session startup.
const quicDialTimeout = 3 * time.Second

// quicCandidateStagger is the pause between starting successive candidate dials
// (happy-eyeballs style): the manager orders candidates by its own preference, so the
// first one usually completes its handshake before a later one even starts, and an
// unreachable one costs at most this much delay rather than the full dial timeout.
const quicCandidateStagger = 250 * time.Millisecond

// quicReprobeInterval is how often quicReprobeLoop retries the QUIC dial after
// quicTunnelProvider trips to fallback, until one succeeds. Deliberately generous: a
// manager restart's own rollout and the forwarder's backend-allowlist refresh both take
// several seconds, so a tight retry interval would just burn probes against a path that
// isn't back yet.
const quicReprobeInterval = 60 * time.Second

// transportUsageTopic is the usage report topic for tunnel transport observability:
// once when startQuicTunnel resolves (quic active, or grpc with a reason), and again
// if quicFallbackProvider later trips (reason "fallback"). Never carries the endpoint
// address; see the usg package doc for what's safe to report.
const transportUsageTopic = "session.transport"

// reportTransport enqueues a transportUsageTopic usage report. reason is omitted for
// a successful QUIC dial (transport == TransportQUIC); every grpc-transport outcome
// carries one. resumed adds a "resumed" entry, and only ever as "true": its mere
// presence answers whether TLS session resumption is landing in the field, so a
// non-resumed dial simply omits the key rather than spelling out "false".
func reportTransport(ctx context.Context, transport, reason string, resumed bool) {
	kv := []string{"transport", transport}
	if reason != "" {
		kv = append(kv, "reason", reason)
	}
	if resumed {
		kv = append(kv, "resumed", "true")
	}
	usg.Quick(ctx, transportUsageTopic, kv...)
}

// quicProbeResult is the outcome of one probeQuicTunnel attempt: conn/addr on success,
// or reason/err on failure. reason is in the same vocabulary as transportUsageTopic
// ("rpc-error", "unimplemented", "disabled", "tls-error", "dial-failed").
type quicProbeResult struct {
	conn   *quic.Conn
	addr   string
	reason string
	err    error
}

// probeQuicTunnel fetches the traffic-manager's QUIC tunnel endpoint descriptor over the
// (healthy, gRPC) manager connection and dials it. Called once at session start by
// startQuicTunnel, and again on every quicReprobeInterval tick by quicReprobeLoop after a
// trip to fallback -- always re-fetched, never re-dialed with a cached TLS config,
// because GetQuicTunnelEndpoint hands out a fresh CA bundle and session-scoped client
// certificate signed by whichever manager process answers the RPC, which after a manager
// restart is not the one the original certificate was issued by. The session's own
// manager connection reconnects on its own after such a restart, so this RPC just works
// again once it does; there is nothing special to do here for that case.
func (s *session) probeQuicTunnel(ctx context.Context) quicProbeResult {
	dialCtx, cancel := context.WithTimeout(ctx, quicDialTimeout)
	defer cancel()

	ep, err := s.managerClient().GetQuicTunnelEndpoint(dialCtx, s.session)
	if err != nil {
		// An older traffic-manager doesn't implement this RPC at all; that's the
		// expected case when QUIC hasn't been rolled out yet, so it's not worth a
		// log line above debug.
		reason := "rpc-error"
		if status.Code(err) == codes.Unimplemented {
			reason = "unimplemented"
		} else {
			clog.Debugf(ctx, "unable to fetch QUIC tunnel endpoint: %v", err)
		}
		return quicProbeResult{reason: reason, err: err}
	}
	if !ep.Enabled {
		clog.Debug(ctx, "traffic-manager has no QUIC tunnel endpoint exposed")
		return quicProbeResult{reason: "disabled", err: errors.New("QUIC tunnel endpoint disabled")}
	}

	tlsConf, err := s.quicTLSConfig(ep)
	if err != nil {
		clog.Infof(ctx, "unable to use QUIC tunnel endpoint: %v", err)
		return quicProbeResult{reason: "tls-error", err: err}
	}

	// The connection is expected to be idle whenever no tunnel streams are active, so
	// it must be kept alive; without this, quic-go's idle timeout tears it down and the
	// session falls back to the port-forwarded transport until the next re-probe.
	// The client always offers EnableDatagrams, but RFC 9221 datagram carriage is opt-in
	// and OFF unless the manager enables it (TELEPRESENCE_QUIC_ENABLE_DATAGRAMS): support
	// is negotiated, so a manager that never offers it means every payload keeps arriving
	// on the stream, exactly as an older manager that can't offer it at all would.
	qCfg := &quic.Config{
		MaxIdleTimeout:  time.Minute,
		KeepAlivePeriod: 15 * time.Second,
		EnableDatagrams: true,
	}
	conn, addr, err := dialQuicCandidates(dialCtx, quicCandidateAddrs(ep), tlsConf, qCfg)
	if err != nil {
		clog.Infof(ctx, "unable to dial QUIC tunnel endpoint: %v", err)
		return quicProbeResult{reason: "dial-failed", err: err}
	}
	return quicProbeResult{conn: conn, addr: addr}
}

// startQuicTunnel opportunistically probes the traffic-manager's QUIC tunnel endpoint
// and, if reachable, activates it. It never returns an error: any problem here (older
// manager, endpoint disabled, unreachable endpoint, bad certificate, ...) just means
// manager-bound tunnel streams stay on the port-forwarded gRPC path, per the design's
// silent-fallback requirement -- quicReprobeLoop only retries after an established
// connection later trips, not after this initial probe fails. Either way, it resolves
// the session's observable transport state and emits the usage report exactly once for
// this attempt.
func (s *session) startQuicTunnel(ctx context.Context) {
	r := s.probeQuicTunnel(ctx)
	if r.err != nil {
		reportTransport(ctx, TransportGRPC, r.reason, false)
		return
	}
	s.activateQuicTunnel(ctx, r.conn, r.addr, "")
}

// activateQuicTunnel installs conn as the session's active QUIC tunnel connection: new
// manager-bound tunnel streams are served over it until a future trip to fallback, and
// agent QUIC connections resolve it as their preferred forwarder candidate (see
// SetPreferredQuicAddr). Used both for the initial dial (reason "") and for a
// post-fallback recovery (reason "reprobe"; see reportTransport).
func (s *session) activateQuicTunnel(ctx context.Context, conn *quic.Conn, addr, reason string) {
	// conn is nil only in tests exercising the reprobe bookkeeping with a scripted
	// prober that never dials a real connection; production always calls this with the
	// connection probeQuicTunnel just dialed.
	var resumed bool
	if conn != nil {
		resumed = conn.ConnectionState().TLS.DidResume
	}
	clog.Debugf(ctx, "QUIC tunnel TLS session resumed: %t", resumed)
	clog.Infof(ctx, "QUIC tunnel transport active (%s)", addr)
	s.quicConn.Store(conn)
	s.setTransportStatus(TransportQUIC, addr)
	s.quicTunnelProvider.Store(newQuicFallbackProvider(ctx, tunnel.NewQuicProvider(conn), func() tunnel.Provider {
		return tunnel.ManagerProvider(s.managerClient())
	}, conn, s.onQuicFallback(ctx)))
	// The receive loop this starts is scoped to conn, not to the session: it exits on
	// its own once conn stops yielding datagrams, which happens both when the session
	// ends (session.stop closes s.quicConn) and when this connection is later
	// superseded by a reprobe (quicFallbackProvider closes it on the first tripped
	// error). s.datagramCounters is session-scoped and accumulates across every
	// connection activateQuicTunnel is ever called with, so the total logged at
	// session end covers every reconnect, not just the last one.
	tunnel.StartDatagramReceiver(ctx, conn, s.datagramCounters)
	reportTransport(ctx, TransportQUIC, reason, resumed)
}

// onQuicFallback returns the callback installed on a freshly activated
// quicFallbackProvider: it downgrades the observable transport state, reports the
// fallback usage event, and wakes quicReprobeLoop with a non-blocking send -- the
// channel is buffered 1 and the loop drains it whenever it's ready, so a trip that
// happens while a probe is already in flight is coalesced into the retry already
// running rather than lost or blocking the tripping goroutine.
func (s *session) onQuicFallback(ctx context.Context) func() {
	return func() {
		s.setTransportStatus(TransportGRPCFallback, "")
		reportTransport(ctx, TransportGRPC, "fallback", false)
		select {
		case s.quicReprobeTrigger <- struct{}{}:
		default:
		}
	}
}

// quicReprober is the shape of a QUIC dial attempt used by quicReprobeLoop: production
// wires probeQuicTunnel, narrowed to conn/addr/err since a re-probe doesn't need
// startQuicTunnel's granular failure reason; tests substitute a fake so the retry
// sequence can be driven deterministically without a real manager or network.
type quicReprober func(ctx context.Context) (conn *quic.Conn, addr string, err error)

// attemptReprobe makes one QUIC re-probe attempt via probe and, on success, activates it
// exactly as the initial dial does (reason "reprobe") and clears any per-agent QUIC
// latches an earlier fallback left behind: agent connections share the manager tunnel's
// endpoint descriptor and candidate address, so they need the same fresh CA/cert this
// probe just fetched (see agentpf.Clients.ResetQuicEndpoint). Returns whether the probe
// succeeded, so a caller retrying on an interval can call this on every tick without
// tracking trip state itself.
func (s *session) attemptReprobe(ctx context.Context, probe quicReprober) bool {
	conn, addr, err := probe(ctx)
	if err != nil {
		return false
	}
	s.activateQuicTunnel(ctx, conn, addr, "reprobe")
	if s.agentClients != nil {
		s.agentClients.ResetQuicEndpoint()
	}
	return true
}

// quicReprobeLoop waits for quicTunnelProvider to trip into fallback (signaled on
// quicReprobeTrigger by onQuicFallback) and retries the QUIC dial every
// quicReprobeInterval until one succeeds, then goes back to waiting for the next trip.
// Existing fallback streams are never migrated to a recovered path: only Tunnel() calls
// made after the swap see it, because managerTunnelProvider always loads the current
// provider fresh. Runs for the lifetime of ctx (the session).
func (s *session) quicReprobeLoop(ctx context.Context) {
	probe := func(ctx context.Context) (*quic.Conn, string, error) {
		r := s.probeQuicTunnel(ctx)
		return r.conn, r.addr, r.err
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.quicReprobeTrigger:
		}
		ticker := time.NewTicker(quicReprobeInterval)
		for recovered := false; !recovered; {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				recovered = s.attemptReprobe(ctx, probe)
			}
		}
		ticker.Stop()
	}
}

// quicTLSConfig builds the client TLS configuration for the QUIC tunnel connection from
// a QuicTunnelEndpoint descriptor: the server is verified against exactly the CA
// bundle handed out over the (RBAC-authenticated) port-forwarded connection, never
// against the system trust store, and the session-scoped client certificate is
// presented so the manager can bind the QUIC connection to the session.
//
// s.quicSessionCache is attached so a later dial against the same manager process can
// resume; it is re-attached on every call (rather than baked in once) because ep, and
// therefore the rest of this config, is re-fetched fresh on every probe -- see
// probeQuicTunnel -- while the cache instance itself stays the same for the life of the
// session.
func (s *session) quicTLSConfig(ep *manager.QuicTunnelEndpoint) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ep.CaPem) {
		return nil, errors.New("no valid CA certificate in QUIC tunnel endpoint descriptor")
	}
	cert, err := tls.X509KeyPair(ep.ClientCertPem, ep.ClientKeyPem)
	if err != nil {
		return nil, fmt.Errorf("invalid client certificate for QUIC tunnel endpoint: %w", err)
	}
	alpn := ep.Alpn
	if alpn == "" {
		alpn = tunnel.QuicALPN
	}
	return &tls.Config{
		RootCAs:            pool,
		ServerName:         ep.ServerName,
		Certificates:       []tls.Certificate{cert},
		NextProtos:         []string{alpn},
		ClientSessionCache: s.quicSessionCache,
	}, nil
}

// quicCandidateAddrs returns the ordered host:port candidates to probe, from
// ep.Candidates when the manager advertised any, falling back to a single candidate
// built from the legacy host/port fields (a manager built before this field existed
// always duplicates the first candidate there anyway; see the proto comment on
// QuicTunnelEndpoint.candidates).
func quicCandidateAddrs(ep *manager.QuicTunnelEndpoint) []string {
	if len(ep.Candidates) == 0 {
		return []string{net.JoinHostPort(ep.Host, strconv.Itoa(int(ep.Port)))}
	}
	addrs := make([]string, len(ep.Candidates))
	for i, c := range ep.Candidates {
		addrs[i] = net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port)))
	}
	return addrs
}

// dialQuicCandidates dials every address in addrs concurrently, starts staggered by
// quicCandidateStagger in list order (happy-eyeballs style), and returns the
// connection and address of the first handshake to complete.
//
// Every dial uses quic.DialAddr, which gives each connection a UDP socket -- and thus
// a client source address -- of its own. That is a requirement, not a convenience:
// the forwarder routes every packet of an established flow by its client source
// address alone, so one socket must never carry more than one QUIC connection (see
// "The forwarder" in docs/reference/quic-transport-architecture.md). A shared
// quic.Transport across dials would violate this. Every other dial --
// whether still stagger-waiting, mid-handshake, or already connected -- is closed in
// the background once a winner is chosen or ctx is done; the caller's ctx bounds how
// long that cleanup can take, not this call, which returns as soon as it has a winner
// or every candidate has failed.
func dialQuicCandidates(ctx context.Context, addrs []string, tlsConf *tls.Config, qCfg *quic.Config) (*quic.Conn, string, error) {
	type dialResult struct {
		conn *quic.Conn
		addr string
		err  error
	}
	results := make(chan dialResult, len(addrs))
	for i, addr := range addrs {
		go func(i int, addr string) {
			if i > 0 {
				select {
				case <-ctx.Done():
					results <- dialResult{addr: addr, err: ctx.Err()}
					return
				case <-time.After(time.Duration(i) * quicCandidateStagger):
				}
			}
			conn, err := quic.DialAddr(ctx, addr, tlsConf, qCfg)
			results <- dialResult{conn: conn, addr: addr, err: err}
		}(i, addr)
	}

	var firstErr error
	for consumed := 1; consumed <= len(addrs); consumed++ {
		r := <-results
		if r.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", r.addr, r.err)
			}
			continue
		}
		// Drain the remaining results off the main path so this call doesn't wait on
		// stragglers; every one of them respects ctx, so this goroutine outlives the
		// caller by at most the remaining dial budget.
		go func(remaining int) {
			for ; remaining > 0; remaining-- {
				if rr := <-results; rr.conn != nil {
					_ = rr.conn.CloseWithError(0, "")
				}
			}
		}(len(addrs) - consumed)
		return r.conn, r.addr, nil
	}
	return nil, "", firstErr
}

// managerTunnelProvider returns the Provider to use for a manager-bound tunnel stream:
// the QUIC-with-fallback wrapper when a QUIC tunnel connection was dialed at session
// start, otherwise the port-forwarded gRPC provider. Agent tunnels are unaffected; they
// never route over QUIC (see design doc, "Where it plugs in").
func (s *session) managerTunnelProvider() tunnel.Provider {
	if p := s.quicTunnelProvider.Load(); p != nil {
		return p
	}
	return tunnel.ManagerProvider(s.managerClient())
}

// quicConnCloser is the subset of *quic.Conn that quicFallbackProvider needs in order
// to shut the connection down once QUIC is marked dead. Narrowed to an interface so
// tests can supply a fake.
type quicConnCloser interface {
	CloseWithError(quic.ApplicationErrorCode, string) error
}

// quicFallbackProvider is a tunnel.Provider that serves calls from a QUIC provider
// while healthy. The first error from the QUIC provider's Tunnel() atomically switches
// the provider to a gRPC fallback: the failed call is retried against the fallback and
// every later call goes straight to it, for as long as this particular
// quicFallbackProvider instance is the one installed on session.quicTunnelProvider. A
// tripped instance never un-trips itself; recovery is external -- onFallback (see
// session.onQuicFallback) wakes quicReprobeLoop, which retries the dial and, on
// success, installs a brand new quicFallbackProvider via
// session.quicTunnelProvider.Store, leaving this tripped instance to be garbage
// collected along with whatever streams were already using its fallback.
type quicFallbackProvider struct {
	logCtx     context.Context
	quic       tunnel.Provider
	fallback   func() tunnel.Provider
	conn       quicConnCloser
	dead       atomic.Bool
	onFallback func()
}

// newQuicFallbackProvider wraps quicP with a fallback to a provider obtained from
// fallback once quicP.Tunnel() first fails; see quicFallbackProvider's doc for how a
// tripped instance is recovered from. onFallback, if non-nil, is invoked
// exactly once at that point (guarded by the same dead-flag CAS that decides whether
// to close conn and log), so callers can use it to update observable state without
// their own synchronization.
func newQuicFallbackProvider(logCtx context.Context, quicP tunnel.Provider, fallback func() tunnel.Provider, conn quicConnCloser, onFallback func()) *quicFallbackProvider {
	return &quicFallbackProvider{
		logCtx:     logCtx,
		quic:       quicP,
		fallback:   fallback,
		conn:       conn,
		onFallback: onFallback,
	}
}

func (p *quicFallbackProvider) Tunnel(ctx context.Context, opts ...grpc.CallOption) (tunnel.GRPCClientStream, error) {
	if p.dead.Load() {
		return p.fallback().Tunnel(ctx, opts...)
	}
	st, err := p.quic.Tunnel(ctx, opts...)
	if err == nil {
		return st, nil
	}
	if ctx.Err() != nil {
		// The caller gave up on this stream; that says nothing about the health of
		// the QUIC path, so it must not trigger the permanent fallback.
		return nil, err
	}
	if p.dead.CompareAndSwap(false, true) {
		clog.Warnf(p.logCtx, "QUIC tunnel transport failed, falling back to port-forwarded gRPC: %v", err)
		if p.conn != nil {
			_ = p.conn.CloseWithError(0, "quic tunnel transport failed")
		}
		if p.onFallback != nil {
			p.onFallback()
		}
	}
	return p.fallback().Tunnel(ctx, opts...)
}

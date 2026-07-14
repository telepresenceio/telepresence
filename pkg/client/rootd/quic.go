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

// transportUsageTopic is the usage report topic for tunnel transport observability:
// once when startQuicTunnel resolves (quic active, or grpc with a reason), and again
// if quicFallbackProvider later trips (reason "fallback"). Never carries the endpoint
// address; see the usg package doc for what's safe to report.
const transportUsageTopic = "session.transport"

// reportTransport enqueues a transportUsageTopic usage report. reason is omitted for
// a successful QUIC dial (transport == TransportQUIC); every grpc-transport outcome
// carries one.
func reportTransport(ctx context.Context, transport, reason string) {
	if reason == "" {
		usg.Quick(ctx, transportUsageTopic, "transport", transport)
	} else {
		usg.Quick(ctx, transportUsageTopic, "transport", transport, "reason", reason)
	}
}

// startQuicTunnel opportunistically fetches the traffic-manager's QUIC tunnel endpoint
// descriptor and, if one is advertised, dials it. It never returns an error: any
// problem here (older manager, endpoint disabled, unreachable endpoint, bad
// certificate, ...) just means manager-bound tunnel streams stay on the
// port-forwarded gRPC path, per the design's silent-fallback requirement. Either way,
// it resolves the session's observable transport state and emits the usage report
// exactly once for this dial attempt.
func (s *session) startQuicTunnel(ctx context.Context) {
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
		reportTransport(ctx, TransportGRPC, reason)
		return
	}
	if !ep.Enabled {
		clog.Debug(ctx, "traffic-manager has no QUIC tunnel endpoint exposed")
		reportTransport(ctx, TransportGRPC, "disabled")
		return
	}

	tlsConf, err := quicTLSConfig(ep)
	if err != nil {
		clog.Infof(ctx, "unable to use QUIC tunnel endpoint: %v", err)
		reportTransport(ctx, TransportGRPC, "tls-error")
		return
	}

	// The connection is expected to be idle whenever no tunnel streams are active, so
	// it must be kept alive; without this, quic-go's idle timeout tears it down and the
	// session permanently falls back to the port-forwarded transport.
	qCfg := &quic.Config{
		MaxIdleTimeout:  time.Minute,
		KeepAlivePeriod: 15 * time.Second,
	}
	conn, addr, err := dialQuicCandidates(dialCtx, quicCandidateAddrs(ep), tlsConf, qCfg)
	if err != nil {
		clog.Infof(ctx, "unable to dial QUIC tunnel endpoint: %v", err)
		reportTransport(ctx, TransportGRPC, "dial-failed")
		return
	}

	clog.Infof(ctx, "QUIC tunnel transport active (%s)", addr)
	s.quicConn.Store(conn)
	s.setTransportStatus(TransportQUIC, addr)
	s.quicTunnelProvider.Store(newQuicFallbackProvider(ctx, tunnel.NewQuicProvider(conn), func() tunnel.Provider {
		return tunnel.ManagerProvider(s.managerClient())
	}, conn, func() {
		s.setTransportStatus(TransportGRPCFallback, "")
		reportTransport(ctx, TransportGRPC, "fallback")
	}))
	reportTransport(ctx, TransportQUIC, "")
}

// quicTLSConfig builds the client TLS configuration for the QUIC tunnel connection from
// a QuicTunnelEndpoint descriptor: the server is verified against exactly the CA
// bundle handed out over the (RBAC-authenticated) port-forwarded connection, never
// against the system trust store, and the session-scoped client certificate is
// presented so the manager can bind the QUIC connection to the session.
func quicTLSConfig(ep *manager.QuicTunnelEndpoint) (*tls.Config, error) {
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
		RootCAs:      pool,
		ServerName:   ep.ServerName,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{alpn},
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
// connection and address of the first handshake to complete. Every other dial --
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
// while healthy. The first error from the QUIC provider's Tunnel() permanently and
// atomically switches the provider to a gRPC fallback for the remainder of the
// session: the failed call is retried against the fallback and every later call goes
// straight to it. There is no background re-probe of the QUIC path in this phase
// (design doc, "Reachability and fallback" defers that to hardening).
type quicFallbackProvider struct {
	logCtx     context.Context
	quic       tunnel.Provider
	fallback   func() tunnel.Provider
	conn       quicConnCloser
	dead       atomic.Bool
	onFallback func()
}

// newQuicFallbackProvider wraps quicP with a permanent fallback to a provider obtained
// from fallback once quicP.Tunnel() first fails. onFallback, if non-nil, is invoked
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

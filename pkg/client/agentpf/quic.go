package agentpf

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// Transport names reported by client.transport, surfaced through Clients.Transports() for
// the `telepresence status` output.
const (
	transportQUIC        = "quic"
	transportPortForward = "grpc"
)

// quicDialTimeout bounds a single opportunistic QUIC dial (or stream-open, for a cached
// connection) to an agent's forwarder-fronted endpoint, so an unreachable forwarder/agent
// cannot delay the port-forward fallback for this dial attempt.
const quicDialTimeout = 3 * time.Second

// quicEndpoint holds the parsed, ready-to-use pieces of a manager.QuicTunnelEndpoint
// descriptor: the forwarder address to dial and a TLS config template. ServerName is
// overridden per agent (its quic_sni), so it is not part of the template. cache is the
// owning clients' session-lifetime tls.ClientSessionCache (see quicEndpointCache),
// shared across every agent SNI so a reconnect to any agent can resume.
type quicEndpoint struct {
	addr  string
	pool  *x509.CertPool
	cert  tls.Certificate
	alpn  string
	cache tls.ClientSessionCache
}

// tlsConfig returns the client TLS configuration to use when dialing this endpoint on
// behalf of the agent identified by sni.
func (e *quicEndpoint) tlsConfig(sni string) *tls.Config {
	return &tls.Config{
		RootCAs:            e.pool,
		ServerName:         sni,
		Certificates:       []tls.Certificate{e.cert},
		NextProtos:         []string{e.alpn},
		ClientSessionCache: e.cache,
	}
}

// fetchQuicEndpoint fetches and parses the traffic-manager's QUIC tunnel endpoint
// descriptor. It returns nil whenever the endpoint cannot be used for agent connections
// (older manager, disabled, or a malformed descriptor); every such case is logged at debug
// or info, never above, because the port-forward path is always fully functional.
//
// preferredAddr, when non-empty, is used as the forwarder address in place of the
// descriptor's own host/port: it is the winning candidate the manager-bound QUIC tunnel
// (pkg/client/rootd/quic.go's startQuicTunnel) already found reachable this session, and
// agent connections go through the same forwarder, just with a different SNI per agent.
// Reusing it avoids racing the candidate list a second time and, more importantly,
// guarantees agent connections dial the address this session is actually observed to work
// through rather than possibly a different candidate that also happens to answer.
func fetchQuicEndpoint(
	ctx context.Context,
	mc manager.ManagerClient,
	session *manager.SessionInfo,
	preferredAddr string,
	cache tls.ClientSessionCache,
) *quicEndpoint {
	dialCtx, cancel := context.WithTimeout(ctx, quicDialTimeout)
	defer cancel()
	ep, err := mc.GetQuicTunnelEndpoint(dialCtx, session)
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			clog.Debug(ctx, "traffic-manager does not implement GetQuicTunnelEndpoint; agent connections will use port-forward")
		} else {
			clog.Debugf(ctx, "unable to fetch QUIC tunnel endpoint for agent connections: %v", err)
		}
		return nil
	}
	if !ep.Enabled {
		clog.Debug(ctx, "traffic-manager has no QUIC tunnel endpoint exposed; agent connections will use port-forward")
		return nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ep.CaPem) {
		clog.Info(ctx, "unable to use QUIC tunnel endpoint for agent connections: no valid CA certificate in descriptor")
		return nil
	}
	cert, err := tls.X509KeyPair(ep.ClientCertPem, ep.ClientKeyPem)
	if err != nil {
		clog.Infof(ctx, "unable to use QUIC tunnel endpoint for agent connections: invalid client certificate: %v", err)
		return nil
	}
	alpn := ep.Alpn
	if alpn == "" {
		alpn = tunnel.QuicALPN
	}
	addr := preferredAddr
	if addr == "" {
		addr = net.JoinHostPort(ep.Host, strconv.Itoa(int(ep.Port)))
	}
	return &quicEndpoint{
		addr:  addr,
		pool:  pool,
		cert:  cert,
		alpn:  alpn,
		cache: cache,
	}
}

// quicEndpointCache lazily fetches the QUIC tunnel endpoint descriptor at most once per
// connector session (on the first dial of an agent that advertises a quic_sni), and caches
// the result -- including the negative result of an unimplemented/disabled/broken
// descriptor -- so that no later agent dial ever repeats the RPC. If no manager client is
// available yet (the agent watch races the very start of the session), the fetch is left
// unattempted rather than cached as a false negative; the next agent dial tries again.
//
// cache is the tls.ClientSessionCache attached to every quicEndpoint this fetches, so a
// reconnect to any agent within the same connector session can resume its TLS session
// regardless of which quicEndpoint instance (pre- or post-reset) served the dial. It is
// set once, by the owning *clients at construction, and is never itself cleared by
// reset(): a stale ticket for a since-restarted manager's CA simply fails to resume and
// falls back to a full handshake against the fresh CA that reset's caller just fetched.
type quicEndpointCache struct {
	mu    sync.Mutex
	ep    *quicEndpoint
	set   bool
	cache tls.ClientSessionCache
}

func (c *quicEndpointCache) get(ctx context.Context, mc manager.ManagerClient, session *manager.SessionInfo, preferredAddr string) *quicEndpoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set {
		return c.ep
	}
	if mc == nil {
		return nil
	}
	c.ep = fetchQuicEndpoint(ctx, mc, session, preferredAddr, c.cache)
	c.set = true
	return c.ep
}

// reset clears the cached descriptor, including a cached negative result, so the next
// get call re-fetches. Used when the rootd session's manager-bound QUIC re-probe
// recovers after a manager restart: the cached descriptor may carry a CA and
// session-scoped client certificate issued by a manager process that no longer exists,
// which would make every agent QUIC dial fail identically until re-fetched.
func (c *quicEndpointCache) reset() {
	c.mu.Lock()
	c.ep = nil
	c.set = false
	c.mu.Unlock()
}

// agentDialer builds the grpc.WithContextDialer function used for one agent's gRPC
// connection: a closure invoked by grpc-go both for the initial dial and for every
// transparent reconnect it makes afterward, on a transport failure, using the same dialer.
//
// When sni is non-empty and quicDead is not yet set, each invocation asks epFor for the
// session's QUIC tunnel endpoint descriptor (nil if QUIC isn't usable this session) and, if
// one is available, tries dialQUIC. Success stores transportQUIC and returns that
// connection. Any failure -- to fetch the descriptor is not a failure, only a nil result;
// but a dialQUIC error -- flips quicDead (exactly once; logFail runs only on that
// transition) and falls through to dialFallback for this and every later invocation, until
// something (e.g. client.refresh, on a new AgentPodInfo) resets quicDead.
//
// This one closure is therefore both the initial-connect path and the whole reconnect
// story: a mid-session QUIC death surfaces as the gRPC connection failing, grpc redials
// using this same dialer, and that redial tries QUIC again exactly once (the forwarder or
// agent may have restarted) before falling back and marking QUIC dead again.
func agentDialer(
	sni string,
	quicDead *atomic.Bool,
	transport *atomic.Value,
	epFor func(ctx context.Context) *quicEndpoint,
	dialQUIC func(ctx context.Context, ep *quicEndpoint, sni string) (net.Conn, error),
	dialFallback func(ctx context.Context, address string) (net.Conn, error),
	logFail func(error),
) func(ctx context.Context, address string) (net.Conn, error) {
	return func(ctx context.Context, address string) (net.Conn, error) {
		if sni != "" && !quicDead.Load() {
			if ep := epFor(ctx); ep != nil {
				c, err := dialQUIC(ctx, ep, sni)
				if err == nil {
					transport.Store(transportQUIC)
					return c, nil
				}
				if quicDead.CompareAndSwap(false, true) && logFail != nil {
					logFail(err)
				}
			}
		}
		transport.Store(transportPortForward)
		return dialFallback(ctx, address)
	}
}

// quicStreamConn adapts a QUIC stream to a net.Conn so it can be handed to grpc.NewClient
// via grpc.WithContextDialer: gRPC's own HTTP/2 framing then rides that single stream for
// the life of the gRPC transport built on it. Read, Write, Close and the deadline methods
// are the stream's; a QUIC stream has no address of its own, so LocalAddr/RemoteAddr are
// taken from the connection it was opened on.
type quicStreamConn struct {
	*quic.Stream
	conn *quic.Conn
}

func newQuicStreamConn(stream *quic.Stream, conn *quic.Conn) net.Conn {
	return &quicStreamConn{Stream: stream, conn: conn}
}

func (c *quicStreamConn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *quicStreamConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// Close fully terminates the stream. quic.Stream.Close finishes only the send
// direction; the receive direction must be released explicitly or the stream never
// terminates and keeps its bookkeeping in quic-go alive for the life of the
// connection.
func (c *quicStreamConn) Close() error {
	c.CancelRead(0)
	return c.Stream.Close()
}

// dialAgentQUIC returns a net.Conn wrapping a freshly opened stream on this agent's cached
// per-agent QUIC connection, dialing that connection first if none is cached yet. Any
// failure -- to dial, to complete the TLS handshake, or to open the stream -- is returned
// to the caller, which is expected to fall back to the port-forward path and mark QUIC dead
// for this agent (see the dialer built in client.dialAgent).
func (ac *client) dialAgentQUIC(ctx context.Context, ep *quicEndpoint, sni string) (net.Conn, error) {
	ac.quicConnMu.Lock()
	conn := ac.quicConn
	ac.quicConnMu.Unlock()

	if conn == nil {
		dialCtx, cancel := context.WithTimeout(ctx, quicDialTimeout)
		defer cancel()
		qCfg := &quic.Config{
			MaxIdleTimeout:  time.Minute,
			KeepAlivePeriod: 15 * time.Second,
		}
		newConn, err := quic.DialAddr(dialCtx, ep.addr, ep.tlsConfig(sni), qCfg)
		if err != nil {
			return nil, fmt.Errorf("dial QUIC endpoint %s (sni %s): %w", ep.addr, sni, err)
		}
		ac.quicConnMu.Lock()
		ac.quicConn = newConn
		ac.quicConnMu.Unlock()
		conn = newConn
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		// This connection is no longer usable; drop it so a later attempt (if any; see
		// quicDead) redials instead of repeatedly failing to open streams on it.
		ac.quicConnMu.Lock()
		if ac.quicConn == conn {
			ac.quicConn = nil
		}
		ac.quicConnMu.Unlock()
		_ = conn.CloseWithError(0, "unable to open stream")
		return nil, fmt.Errorf("open QUIC stream to %s (sni %s): %w", ep.addr, sni, err)
	}
	return newQuicStreamConn(stream, conn), nil
}

// closeQuicConn closes and clears this agent's cached QUIC connection, if any. Called when
// the agent client is torn down (pod removed, replaced, or the connection cancelled) and
// when refresh gives a dead QUIC path a fresh chance following an AgentPodInfo update.
func (ac *client) closeQuicConn() {
	ac.quicConnMu.Lock()
	conn := ac.quicConn
	ac.quicConn = nil
	ac.quicConnMu.Unlock()
	if conn != nil {
		_ = conn.CloseWithError(0, "agent client closed")
	}
}

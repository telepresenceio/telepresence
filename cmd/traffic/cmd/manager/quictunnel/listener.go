package quictunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// datagramStatsInterval is how often Serve logs the listener-wide datagram counters.
// There is no existing periodic stats log in the manager to piggyback on for this, so
// this mirrors the quicforwarder's own metricsLogInterval cadence (see
// cmd/traffic/cmd/quicforwarder/forwarder.go) rather than inventing an unrelated one.
const datagramStatsInterval = 30 * time.Second

// quic stream error codes used to reject a stream instead of letting it proceed to
// the tunnel handler. The values only need to be distinct; quic-go delivers them to
// the peer as the StreamError.ErrorCode of a failed Read/Write.
const (
	errHandshakeFailed     quic.StreamErrorCode = 1
	errSessionCertMismatch quic.StreamErrorCode = 2
)

// TunnelHandler is the manager-side entry point a Listener hands an accepted,
// session-verified tunnel stream to. Its signature matches (*state.State).Tunnel, so
// production code can pass that method directly; tests can inject a fake without
// constructing a *state.State.
type TunnelHandler func(ctx context.Context, stream tunnel.Stream) error

// Listener accepts mTLS-authenticated QUIC connections on behalf of the
// traffic-manager. Every bidirectional stream on an accepted connection is framed as
// a tunnel.Stream and, once its declared session ID has been checked against the
// connection's client certificate CommonName, handed to a TunnelHandler.
type Listener struct {
	ln       *quic.Listener
	conn     net.PacketConn
	handler  TunnelHandler
	datagram *tunnel.DatagramCounters
}

// Listen starts a QUIC listener on 0.0.0.0:port, running behind the packet forwarder
// described in docs/reference/quic-transport-architecture.md ("The forwarder"). podIP is this
// manager's own pod IP; the listener is built on a quic.Transport configured with a
// quicfwd.CIDGenerator for podIP, so every connection ID it hands out -- not just the
// one used during the handshake -- decodes back to this pod via quicfwd.DecodeCID. That
// is what lets the forwarder route every packet after a connection's first straight to
// this listener with no flow table of its own.
//
// Clients must present a certificate that verifies against ca; the listener presents
// serverCert. handler is invoked, once per accepted stream, with the stream already
// verified to belong to the session named by that stream's peer certificate.
func Listen(port uint16, podIP netip.Addr, ca *CA, serverCert tls.Certificate, handler TunnelHandler) (*Listener, error) {
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    ca.Pool(),
		NextProtos:   []string{tunnel.QuicALPN},
	}
	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port)))
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("quictunnel: resolve %s: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("quictunnel: listen on %s: %w", addr, err)
	}
	// Clients keep otherwise-idle connections alive with pings every 15s; the idle
	// timeout only needs to be comfortably above that ping interval. Every flow the
	// client routes through the VPN is one concurrent stream, so the stream limit
	// must accommodate a busy client, not quic-go's default of 100. EnableDatagrams
	// lets a UDP flow's payload ride an unreliable QUIC datagram instead of its
	// stream when the client negotiated it too; an older client that didn't simply
	// never sends one and every payload keeps arriving on the stream as before.
	qCfg := &quic.Config{
		MaxIdleTimeout:     time.Minute,
		MaxIncomingStreams: tunnel.QuicMaxIncomingStreams,
		EnableDatagrams:    !datagramsDisabledByEnv(),
	}
	tr := &quic.Transport{
		Conn:                  conn,
		ConnectionIDGenerator: quicfwd.NewCIDGenerator(podIP),
	}
	ln, err := tr.Listen(tlsConf, qCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("quictunnel: listen on %s: %w", addr, err)
	}
	return &Listener{ln: ln, conn: conn, handler: handler, datagram: &tunnel.DatagramCounters{}}, nil
}

// datagramsDisabledByEnv reports whether TELEPRESENCE_QUIC_DISABLE_DATAGRAMS is set to a
// truthy value (strconv.ParseBool). RFC 9221 datagram support is negotiated per QUIC
// connection from what each end offers in its quic.Config, so a listener that never offers
// EnableDatagrams makes the negotiated result false for every client regardless of what the
// client itself offered: this single flag turns datagram carriage off bilaterally for every
// connection this listener accepts, without any client-side change. Unset (the default)
// leaves EnableDatagrams on.
func datagramsDisabledByEnv() bool {
	disabled, _ := strconv.ParseBool(os.Getenv("TELEPRESENCE_QUIC_DISABLE_DATAGRAMS"))
	return disabled
}

// Addr returns the listener's local address.
func (l *Listener) Addr() net.Addr {
	return l.ln.Addr()
}

// Close closes the underlying QUIC listener without waiting for accepted connections
// to drain, then closes the transport's UDP socket. Serve's shutdown path calls this
// via ctx cancellation; direct callers (e.g. tests) may call it to force an
// in-progress Serve to return.
func (l *Listener) Close() error {
	err := l.ln.Close()
	if cerr := l.conn.Close(); err == nil {
		err = cerr
	}
	return err
}

// Serve runs the accept loop until ctx is done, at which point it closes the listener
// and every connection and stream it accepted, then returns nil. A problem with an
// individual peer (a failed handshake, an unauthenticated connection, a session ID
// that doesn't match the presented certificate) is logged and never terminates Serve.
func (l *Listener) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		_ = l.ln.Close()
	}()
	go l.logDatagramStatsLoop(ctx)
	for {
		conn, err := l.ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, quic.ErrServerClosed) {
				return nil
			}
			clog.Errorf(ctx, "quictunnel: accept failed: %v", err)
			continue
		}
		go l.handleConn(ctx, conn)
	}
}

// logDatagramStatsLoop periodically logs the listener-wide datagram counters -- summed
// across every client connection this Listener has ever accepted, since there is no
// natural per-connection lifecycle hook shorter than the manager's own -- so the
// datagram feature is observable in the field without per-flow logging.
func (l *Listener) logDatagramStatsLoop(ctx context.Context) {
	ticker := time.NewTicker(datagramStatsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			clog.Debugf(ctx, "quictunnel: datagram counters: %s", l.datagram)
		}
	}
}

func (l *Listener) handleConn(ctx context.Context, conn *quic.Conn) {
	cn, err := peerCommonName(conn)
	if err != nil {
		clog.Errorf(ctx, "quictunnel: rejecting connection from %s: %v", conn.RemoteAddr(), err)
		_ = conn.CloseWithError(0, "no verified client certificate")
		return
	}
	tunnel.StartDatagramReceiver(ctx, conn, l.datagram)
	for {
		qs, err := conn.AcceptStream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				_ = conn.CloseWithError(0, "traffic-manager shutting down")
			}
			return
		}
		go l.handleStream(ctx, conn, qs, cn)
	}
}

func (l *Listener) handleStream(ctx context.Context, conn *quic.Conn, qs *quic.Stream, certCN string) {
	// A QUIC stream only terminates -- and only returns its stream-limit credit to
	// the peer -- once both directions have finished. Close finishes the send
	// direction; CancelRead releases the receive direction even when the client's
	// FIN was never read (the tunnel protocol ends conversations with a closeSend
	// message, not at transport EOF). Without both, every completed tunnel stream
	// leaks its credit and after MaxIncomingStreams of them the client can no
	// longer open any stream on the connection.
	defer func() {
		_ = qs.Close()
		qs.CancelRead(0)
	}()
	stream, err := tunnel.NewServerStream(ctx, tunnel.ClientToManager, tunnel.NewQuicServerStream(conn, qs))
	if err != nil {
		clog.Errorf(ctx, "quictunnel: stream handshake failed: %v", err)
		qs.CancelWrite(errHandshakeFailed)
		qs.CancelRead(errHandshakeFailed)
		return
	}
	if sid := string(stream.SessionID()); sid != certCN {
		clog.Errorf(ctx, "quictunnel: stream session id %q does not match client certificate CN %q; closing", sid, certCN)
		qs.CancelWrite(errSessionCertMismatch)
		qs.CancelRead(errSessionCertMismatch)
		return
	}
	if stream.ID().Protocol() == types.ProtoUDP {
		detach := tunnel.AttachDatagramRoute(stream)
		defer detach()
	}
	if err := l.handler(ctx, stream); err != nil && ctx.Err() == nil {
		clog.Errorf(ctx, "quictunnel: tunnel for session %s ended with error: %v", certCN, err)
	}
}

func peerCommonName(conn *quic.Conn) (string, error) {
	state := conn.ConnectionState().TLS
	if len(state.PeerCertificates) == 0 {
		return "", errors.New("no peer certificate presented")
	}
	return state.PeerCertificates[0].Subject.CommonName, nil
}

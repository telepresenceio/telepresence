package quictunnel

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

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
	ln      *quic.Listener
	conn    net.PacketConn
	handler TunnelHandler
}

// Listen starts a QUIC listener on 0.0.0.0:port, running behind the packet forwarder
// described in docs/plans/quic-transport/design.md ("The forwarder"). podIP is this
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
	// timeout only needs to be comfortably above that ping interval.
	qCfg := &quic.Config{MaxIdleTimeout: time.Minute}
	tr := &quic.Transport{
		Conn:                  conn,
		ConnectionIDGenerator: quicfwd.NewCIDGenerator(podIP),
	}
	ln, err := tr.Listen(tlsConf, qCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("quictunnel: listen on %s: %w", addr, err)
	}
	return &Listener{ln: ln, conn: conn, handler: handler}, nil
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

func (l *Listener) handleConn(ctx context.Context, conn *quic.Conn) {
	cn, err := peerCommonName(conn)
	if err != nil {
		clog.Errorf(ctx, "quictunnel: rejecting connection from %s: %v", conn.RemoteAddr(), err)
		_ = conn.CloseWithError(0, "no verified client certificate")
		return
	}
	for {
		qs, err := conn.AcceptStream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				_ = conn.CloseWithError(0, "traffic-manager shutting down")
			}
			return
		}
		go l.handleStream(ctx, qs, cn)
	}
}

func (l *Listener) handleStream(ctx context.Context, qs *quic.Stream, certCN string) {
	stream, err := tunnel.NewServerStream(ctx, tunnel.ClientToManager, tunnel.NewQuicServerStream(qs))
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

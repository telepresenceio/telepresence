// Package quicserver implements the traffic-agent side of the QUIC listener described in
// "Agent connections over QUIC" (docs/plans/quic-transport/design.md). It binds a QUIC
// transport to the agent's own pod IP, authenticated with a certificate the agent fetches
// from the traffic-manager over its own session (see cmd/traffic/cmd/agent's use of the
// GetQuicAgentCert RPC), and feeds every accepted QUIC stream into the agent's existing
// *grpc.Server as if it had been accepted on an ordinary net.Listener.
//
// This is deliberately not shared with cmd/traffic/cmd/manager/quictunnel, whose Listener
// frames a single purpose-built TunnelHandler callback over each QUIC stream. The agent's
// gRPC server exposes its whole service surface (Version, Lookup, Tunnel, WatchDial, ...)
// unmodified, so instead of bespoke framing this package hands grpc.Server.Serve a
// net.Listener whose Accept returns one net.Conn per accepted QUIC stream.
package quicserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"google.golang.org/grpc"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// Material is the TLS server certificate and client CA pool a Listener presents and
// verifies against. A fresh Material is minted by the traffic-manager's
// GetQuicAgentCert RPC every time the agent's manager session is (re-)established --
// a new manager process means a new CA -- and installed into a running Listener with
// SetMaterial, which takes effect for every subsequent handshake without restarting
// the listener.
type Material struct {
	Cert tls.Certificate
	Pool *x509.CertPool
}

// ParseMaterial decodes the PEM-encoded certificate, key, and CA returned by
// GetQuicAgentCert into a Material.
func ParseMaterial(certPEM, keyPEM, caPEM []byte) (Material, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return Material{}, fmt.Errorf("quicserver: parse server certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return Material{}, errors.New("quicserver: no CA certificates found in PEM")
	}
	return Material{Cert: cert, Pool: pool}, nil
}

// Listener accepts mTLS-authenticated QUIC connections on behalf of the traffic-agent
// and serves the agent's gRPC server over every stream any of them accepts.
type Listener struct {
	conn       net.PacketConn
	transport  *quic.Transport
	qln        *quic.Listener
	grpcServer *grpc.Server
	material   atomic.Pointer[Material]
}

// New starts a QUIC listener on 0.0.0.0:port, running behind the packet forwarder
// described in "The forwarder" (docs/plans/quic-transport/design.md). podIP is this
// agent's own pod IP; the listener is built on a quic.Transport configured with a
// quicfwd.CIDGenerator for podIP, so every connection ID it hands out decodes back to
// this pod via quicfwd.DecodeCID -- what lets the forwarder route every packet after a
// connection's first straight to this listener with no flow table of its own.
//
// grpcServer is the agent's existing *grpc.Server; New does not register any service
// on it and does not start serving -- call Serve for that. initial is the Material to
// present until a later SetMaterial call replaces it.
func New(podIP netip.Addr, port uint16, grpcServer *grpc.Server, initial Material) (*Listener, error) {
	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(int(port)))
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("quicserver: resolve %s: %w", addr, err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("quicserver: listen on %s: %w", addr, err)
	}
	l := &Listener{conn: conn, grpcServer: grpcServer}
	l.material.Store(&initial)

	tlsConf := &tls.Config{
		NextProtos:         []string{tunnel.QuicALPN},
		GetConfigForClient: l.getConfigForClient,
	}
	// Clients keep otherwise-idle connections alive with pings well inside this
	// window; see cmd/traffic/cmd/manager/quictunnel.Listen for the equivalent
	// manager-side choices, including why the stream limit is raised above
	// quic-go's default of 100.
	qCfg := &quic.Config{MaxIdleTimeout: time.Minute, MaxIncomingStreams: tunnel.QuicMaxIncomingStreams}
	tr := &quic.Transport{
		Conn:                  conn,
		ConnectionIDGenerator: quicfwd.NewCIDGenerator(podIP),
	}
	qln, err := tr.Listen(tlsConf, qCfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("quicserver: listen on %s: %w", addr, err)
	}
	l.transport = tr
	l.qln = qln
	return l, nil
}

// getConfigForClient is installed as the base tls.Config's GetConfigForClient so that
// SetMaterial can swap the certificate and client CA pool a running Listener uses
// without tearing down the QUIC transport: Go's TLS stack calls this once per
// handshake and uses the returned Config in place of the original.
//
// Returning a fresh *tls.Config on every call does not defeat TLS session-ticket
// resumption: crypto/tls derives the auto-rotated ticket-encryption keys from the base
// Config passed to Listen (the one holding GetConfigForClient), not from whatever this
// method returns, and a resumed ticket's embedded client certificate is independently
// re-verified against this Config's current ClientCAs -- so a ticket minted under a
// since-replaced Material's CA (see SetMaterial) fails that re-verification and falls
// back to a full handshake, without needing this method to track ticket-key state
// itself.
func (l *Listener) getConfigForClient(*tls.ClientHelloInfo) (*tls.Config, error) {
	m := l.material.Load()
	return &tls.Config{
		Certificates: []tls.Certificate{m.Cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    m.Pool,
		NextProtos:   []string{tunnel.QuicALPN},
	}, nil
}

// SetMaterial installs new TLS material for every handshake from this point on.
// Connections already established are unaffected; the swap is atomic and requires no
// listener restart, which is what lets the agent re-fetch its certificate after a
// manager reconnect (new manager, new CA) without dropping in-flight QUIC traffic.
func (l *Listener) SetMaterial(m Material) {
	l.material.Store(&m)
}

// Addr returns the listener's local address.
func (l *Listener) Addr() net.Addr {
	return l.qln.Addr()
}

// Close closes the underlying QUIC listener without waiting for accepted connections
// to drain, then closes the transport's UDP socket.
func (l *Listener) Close() error {
	err := l.qln.Close()
	if cerr := l.conn.Close(); err == nil {
		err = cerr
	}
	return err
}

// Serve runs the QUIC accept loop and, concurrently, l.grpcServer.Serve over a
// net.Listener fed by every bidirectional stream any accepted connection opens. It
// returns nil once ctx is done, after closing the QUIC listener and waiting for the
// grpc.Server.Serve call on this listener to return -- which affects only this
// listener, not any other the same *grpc.Server may be serving concurrently (e.g. the
// agent's plain TCP listener), since grpc.Server.Serve is scoped per net.Listener.
func (l *Listener) Serve(ctx context.Context) error {
	streamLn := newStreamListener(l.qln.Addr())
	grpcDone := make(chan error, 1)
	go func() { grpcDone <- l.grpcServer.Serve(streamLn) }()

	go func() {
		<-ctx.Done()
		_ = l.qln.Close()
	}()

	for {
		conn, err := l.qln.Accept(ctx)
		if err != nil {
			break
		}
		go l.handleConn(ctx, conn, streamLn)
	}
	streamLn.Close()
	if err := <-grpcDone; err != nil && ctx.Err() == nil {
		clog.Errorf(ctx, "quicserver: grpc serve on QUIC listener ended with error: %v", err)
	}
	return nil
}

// handleConn accepts every bidirectional stream conn opens for as long as conn lives,
// wrapping each as a net.Conn and pushing it to streamLn for grpc.Server.Serve to pick
// up. TLS's RequireAndVerifyClientCert already rejected any connection whose client
// certificate doesn't chain to the CA in the Material active at handshake time, so no
// further per-stream authentication happens here -- any client cert signed by that CA
// is, by construction, a legitimate short-lived session credential (see "Agent
// connections over QUIC" in docs/plans/quic-transport/design.md).
func (l *Listener) handleConn(ctx context.Context, conn *quic.Conn, streamLn *streamListener) {
	for {
		qs, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		sc := &streamConn{Stream: qs, local: conn.LocalAddr(), remote: conn.RemoteAddr()}
		if !streamLn.push(ctx, sc) {
			_ = sc.Close()
			return
		}
	}
}

// streamConn adapts a *quic.Stream to net.Conn: quic.Stream already implements
// Read/Write/Close/SetDeadline/SetReadDeadline/SetWriteDeadline, so only the address
// accessors -- which belong to the connection, not the stream -- need to be supplied.
type streamConn struct {
	*quic.Stream
	local, remote net.Addr
}

func (c *streamConn) LocalAddr() net.Addr  { return c.local }
func (c *streamConn) RemoteAddr() net.Addr { return c.remote }

// Close fully terminates the stream. quic.Stream.Close finishes only the send
// direction; the receive direction must be released explicitly or the stream never
// terminates, never returns its stream-limit credit to the peer, and keeps its
// bookkeeping in quic-go alive for the life of the connection.
func (c *streamConn) Close() error {
	c.CancelRead(0)
	return c.Stream.Close()
}

// streamListener is a net.Listener whose Accept returns connections pushed to it by
// push, rather than accepted from any socket of its own. It is what lets a
// *grpc.Server -- which only knows how to Serve a net.Listener -- run over streams
// multiplexed inside QUIC connections.
type streamListener struct {
	addr      net.Addr
	connCh    chan net.Conn
	closeOnce sync.Once
	closed    chan struct{}
}

func newStreamListener(addr net.Addr) *streamListener {
	return &streamListener{addr: addr, connCh: make(chan net.Conn), closed: make(chan struct{})}
}

// push delivers c to a pending Accept call. It returns false, closing nothing itself,
// if the listener is closed or ctx is done before that happens; the caller is
// responsible for closing c in that case.
func (s *streamListener) push(ctx context.Context, c net.Conn) bool {
	select {
	case s.connCh <- c:
		return true
	case <-s.closed:
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *streamListener) Accept() (net.Conn, error) {
	select {
	case c := <-s.connCh:
		return c, nil
	case <-s.closed:
		return nil, net.ErrClosed
	}
}

func (s *streamListener) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

func (s *streamListener) Addr() net.Addr {
	return s.addr
}

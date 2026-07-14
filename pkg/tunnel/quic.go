package tunnel

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/quic-go/quic-go"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// QuicALPN is the ALPN protocol negotiated between client and manager for the QUIC tunnel
// transport. Both ends of the handshake must offer/accept exactly this value.
const QuicALPN = "tp-tunnel"

// QuicMaxIncomingStreams is the concurrent-stream limit the manager's and agent's QUIC
// listeners grant a client connection. Every flow routed through the tunnel is one
// concurrent stream, so this must accommodate a busy client; quic-go's default of 100
// is far too small.
const QuicMaxIncomingStreams = 4096

// maxFrameSize is the largest TunnelMessage frame that will be sent or accepted on a QUIC
// stream. It exists to bound how much memory a single frame length prefix can commit us to
// allocating before the payload has even arrived.
const maxFrameSize = 4 * 1024 * 1024 // 4 MiB

// readFrame reads one length-prefixed frame from r. A clean end of stream at a frame boundary
// is reported as io.EOF; a stream end in the middle of a frame is reported as
// io.ErrUnexpectedEOF so that callers can tell a graceful close from a truncated message.
func readFrame(r io.Reader) ([]byte, error) {
	var lb [4]byte
	if _, err := io.ReadFull(r, lb[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(lb[:])
	if n > maxFrameSize {
		return nil, fmt.Errorf("quic tunnel frame of %d bytes exceeds maximum of %d bytes", n, maxFrameSize)
	}
	b := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, b); err != nil {
			if err == io.EOF { //nolint:errorlint // io.ReadFull only ever returns io.EOF verbatim, never wrapped
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
	}
	return b, nil
}

// writeFrame writes b to w as one length-prefixed frame.
func writeFrame(w io.Writer, b []byte) error {
	if len(b) > maxFrameSize {
		return fmt.Errorf("quic tunnel frame of %d bytes exceeds maximum of %d bytes", len(b), maxFrameSize)
	}
	fb := make([]byte, 4+len(b))
	binary.BigEndian.PutUint32(fb, uint32(len(b)))
	copy(fb[4:], b)
	_, err := w.Write(fb)
	return err
}

// quicStream frames TunnelMessages onto a *quic.Stream. Send is safe for concurrent use; the
// underlying quic.Stream is not, so writes (including the write-side close done by
// quicClientStream.CloseSend) are serialized through sendMu. conn is the *quic.Conn the
// stream was opened or accepted on; it is what makes quicStream a DatagramCapable, giving
// the transport-agnostic stream type access to the connection's unreliable datagrams
// without depending on quic-go itself.
type quicStream struct {
	stream *quic.Stream
	conn   *quic.Conn
	sendMu sync.Mutex
}

// SupportsDatagrams reports whether both ends of the connection carrying this stream
// negotiated RFC 9221 datagram support.
func (s *quicStream) SupportsDatagrams() bool {
	sd := s.conn.ConnectionState().SupportsDatagrams
	return sd.Local && sd.Remote
}

// SendDatagram sends payload as an unreliable QUIC datagram on the connection carrying
// this stream.
func (s *quicStream) SendDatagram(payload []byte) error {
	return s.conn.SendDatagram(payload)
}

// Conn returns the QUIC connection carrying this stream.
func (s *quicStream) Conn() *quic.Conn {
	return s.conn
}

func (s *quicStream) Send(m *rpc.TunnelMessage) error {
	b, err := proto.Marshal(m)
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return writeFrame(s.stream, b)
}

func (s *quicStream) Recv() (*rpc.TunnelMessage, error) {
	b, err := readFrame(s.stream)
	if err != nil {
		return nil, err
	}
	m := new(rpc.TunnelMessage)
	if err := proto.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("failed to unmarshal QUIC tunnel frame: %w", err)
	}
	return m, nil
}

// CloseRecv releases the receive direction of the underlying QUIC stream. The tunnel
// protocol ends a conversation with a closeSend message rather than by reading the
// transport stream to EOF, so without this explicit cancel the receive direction --
// and with it the stream's bookkeeping in quic-go, and on the server the stream-limit
// credit owed back to the client -- would linger until the connection closes.
func (s *quicStream) CloseRecv() {
	s.stream.CancelRead(0)
}

// quicClientStream is the client side of a QUIC tunnel stream.
type quicClientStream struct {
	quicStream
}

// NewQuicClientStream wraps a QUIC stream opened by the client as a GRPCClientStream.
// conn is the connection s was opened on.
func NewQuicClientStream(conn *quic.Conn, s *quic.Stream) GRPCClientStream {
	return &quicClientStream{quicStream{stream: s, conn: conn}}
}

// CloseSend closes the write direction of the underlying QUIC stream; the read direction stays
// open so a peer response already in flight can still be received.
func (s *quicClientStream) CloseSend() error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	// The only error quic.Stream.Close reports is that the send direction was already
	// canceled -- locally, or by the STOP_SENDING a peer sends when it finishes the
	// stream first. The direction is terminated either way, which is all CloseSend
	// promises, so that error is not propagated.
	_ = s.stream.Close()
	return nil
}

// NewQuicServerStream wraps a QUIC stream accepted by the manager as a GRPCStream.
// conn is the connection s was accepted on.
func NewQuicServerStream(conn *quic.Conn, s *quic.Stream) GRPCStream {
	return &quicStream{stream: s, conn: conn}
}

// quicProvider is a Provider that opens tunnel streams on a QUIC connection.
type quicProvider struct {
	conn *quic.Conn
}

// NewQuicProvider returns a Provider that opens a new bidirectional QUIC stream for each
// call to Tunnel. The passed grpc.CallOptions are not applicable to QUIC and are ignored.
func NewQuicProvider(conn *quic.Conn) Provider {
	return quicProvider{conn: conn}
}

func (p quicProvider) Tunnel(ctx context.Context, _ ...grpc.CallOption) (GRPCClientStream, error) {
	s, err := p.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return NewQuicClientStream(p.conn, s), nil
}

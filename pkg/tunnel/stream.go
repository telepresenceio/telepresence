package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// Version of the stream protocol.
//
//	0 which didn't report versions and didn't do synchronization
//	1 used MuxTunnel instead of one tunnel per connection.
const Version = uint16(2)

type (
	SessionID string
	Tag       string
)

const (
	TunToClient     = Tag("TUN⇄CLI")
	TunToDNS        = Tag("TUN⇄DNS")
	DnsToTun        = Tag("DNS⇄TUN")
	TunToLocal      = Tag("TUN⇄LCL")
	LocalToTun      = Tag("LCL⇄TUN")
	ClientToAgent   = Tag("CLI⇄AGN")
	ClientToDNS     = Tag("CLI⇄DNS")
	AgentToClient   = Tag("AGN⇄CLI")
	AgentToProxied  = Tag("AGN⇄PRX")
	ClientToManager = Tag("CLI⇄MGR")
	ManagerToClient = Tag("MGR⇄CLI")
)

// Endpoint is an endpoint for a Stream such as a Dialer or a bidirectional pipe.
type Endpoint interface {
	Start(ctx context.Context)
	Done() <-chan struct{}
}

// GRPCStream is the bare minimum needed for reading and writing TunnelMessages
// on a Manager_TunnelServer or Manager_TunnelClient.
type GRPCStream interface {
	Recv() (*rpc.TunnelMessage, error)
	Send(*rpc.TunnelMessage) error
}

// RecvCloser is optionally implemented by GRPCStream transports whose receive
// direction must be released explicitly when the tunnel conversation ends at the
// message level (a closeSend message) rather than by reading the transport stream
// to EOF. gRPC streams don't need this -- the RPC's own termination cleans up --
// but a QUIC stream's receive direction would otherwise linger until the
// connection closes.
type RecvCloser interface {
	CloseRecv()
}

// GRPCContextStream is similar to GRPCStream but also allows the caller to pass a context.Context
// to Recv and Send so that the stream can be canceled.
type GRPCContextStream interface {
	RecvContext(context.Context) (*rpc.TunnelMessage, error)
	SendContext(context.Context, *rpc.TunnelMessage) error
}

// The Stream interface represents a bidirectional, synchronized connection Tunnel
// that sends TCP or UDP traffic over gRPC using manager.TunnelMessage messages.
//
// A Stream is closed by one of six things happening at either end (or at both ends).
//
//  1. Read from local connection fails (typically EOF)
//  2. Write to local connection fails (connection peer closed)
//  3. Idle timer timed out.
//  4. Context is cancelled.
//  5. closeSend request received from Tunnel peer.
//  6. Disconnect received from Tunnel peer.
//
// When #1 or #2 happens, the Stream will either call CloseSend() (if it's a client Stream)
// or send a closeSend request (if it's a StreamServer) to its Stream peer, shorten the
// Idle timer, and then continue to serve incoming data from the Stream peer until it's
// closed or a Disconnect is received. Once that happens, it's guaranteed that the Tunnel
// peer will send no more messages and the Stream is closed.
//
// When #3, #4, or #5 happens, the Tunnel will send a Disconnect to its Stream peer and close.
//
// When #6 happens, the Stream will simply close.
type Stream interface {
	Tag() Tag
	ID() ConnID
	Receive(context.Context) (Message, error)
	Send(context.Context, Message) error
	CloseSend(ctx context.Context) error
	PeerVersion() uint16
	SessionID() SessionID
	DialTimeout() time.Duration
	RoundtripLatency() time.Duration
	SetTag(tag Tag)
}

// StreamCreator is a function that creats a Stream.
type StreamCreator func(context.Context, ConnID) (Stream, error)

// ReadLoop reads from the Stream and dispatches messages and error to the give channels. There
// will be max one error since the error also terminates the loop.
func ReadLoop(ctx context.Context, s Stream, p *CounterProbe) (<-chan Message, <-chan error) {
	msgCh := make(chan Message, 50)
	errCh := make(chan error, 1) // Max one message will be sent on this channel
	clog.Tracef(ctx, "<- %s %s, ReadLoop starting", s.Tag(), s.ID())
	go func() {
		var endReason string
		defer func() {
			close(errCh)
			close(msgCh)
			clog.Tracef(ctx, "<- %s %s, ReadLoop ended: %s", s.Tag(), s.ID(), endReason)
		}()

		for {
			m, err := s.Receive(ctx)
			if m != nil && p != nil {
				p.Increment(uint64(len(m.Payload())))
			}

			switch {
			case err == nil:
				select {
				case <-ctx.Done():
					endReason = ctx.Err().Error()
				case msgCh <- m:
					continue
				}
			case ctx.Err() != nil:
				endReason = ctx.Err().Error()
			case errors.Is(err, io.EOF):
				endReason = "EOF on input"
			case errors.Is(err, net.ErrClosed):
				endReason = "stream closed"
			case errors.Is(err, context.Canceled):
				endReason = err.Error()
			default:
				switch status.Code(err) {
				case codes.NotFound:
					endReason = "session closed"
				case codes.Canceled:
					endReason = err.Error()
				case codes.Unavailable:
					if strings.HasSuffix(err.Error(), "reading from server: EOF") {
						endReason = err.Error()
						break
					}
					fallthrough
				default:
					endReason = err.Error()
					select {
					case errCh <- fmt.Errorf("<! %s %s, read from grpc.ClientStream failed: %w", s.Tag(), s.ID(), err):
					default:
					}
				}
			}
			break
		}
	}()
	return msgCh, errCh
}

// WriteLoop reads messages from the channel and writes them to the Stream. It will call CloseSend() on the
// stream when the channel is closed.
func WriteLoop(
	ctx context.Context,
	s Stream, msgCh <-chan Message,
	wg *sync.WaitGroup,
	p *CounterProbe,
) {
	clog.Tracef(ctx, "-> %s %s, WriteLoop starting", s.Tag(), s.ID())
	go func() {
		var endReason string
		defer func() {
			clog.Tracef(ctx, "   %s %s, WriteLoop ended: %s", s.Tag(), s.ID(), endReason)
			if err := s.CloseSend(ctx); err != nil {
				clog.Errorf(ctx, "!> %s %s, Send of closeSend failed: %v", s.Tag(), s.ID(), err)
			}
			wg.Done()
		}()
		for {
			select {
			case <-ctx.Done():
				endReason = ctx.Err().Error()
			case m, ok := <-msgCh:
				if !ok {
					endReason = "input channel is closed"
					break
				}

				err := s.Send(ctx, m)
				if m != nil && p != nil {
					p.Increment(uint64(len(m.Payload())))
				}

				switch {
				case err == nil:
					continue
				case errors.Is(err, net.ErrClosed):
					endReason = "output stream is closed"
				default:
					endReason = err.Error()
					clog.Errorf(ctx, "!! %s %s, Send failed: %v", s.Tag(), s.ID(), err)
				}
			}
			break
		}
	}()
}

type stream struct {
	grpcStream       GRPCStream
	id               ConnID
	dialTimeout      time.Duration
	roundtripLatency time.Duration
	sessionID        SessionID
	tag              Tag
	syncRatio        uint32 // send and check sync after each syncRatio message
	ackWindow        uint32 // maximum permitted difference between sent and received ack
	peerVersion      uint16

	// datagrams, when non-nil (set by AttachDatagramRoute), delivers payloads that
	// arrived as QUIC datagrams for this flow's ConnID. Receive merges it with
	// transport-carried messages so a UDP flow that is also getting some payloads over
	// unreliable datagrams still presents as one ordinary message stream to its caller.
	datagrams        chan []byte
	datagramCounters *DatagramCounters

	// pumpOnce and pumpCh back the transport-receive goroutine Receive starts the first
	// time it is called on a stream with a non-nil datagrams channel: a single blocking
	// Receive call can't select against a channel, so a goroutine pumps the transport
	// into pumpCh instead, letting Receive select between it and datagrams.
	pumpOnce sync.Once
	pumpCh   chan recvResult
}

// recvResult is one result of the transport-receive goroutine Receive spawns for a
// datagram-attached stream.
type recvResult struct {
	m   Message
	err error
}

func newStream(tag Tag, grpcStream GRPCStream) stream {
	return stream{tag: tag, grpcStream: grpcStream, syncRatio: 8, ackWindow: 1}
}

func (s *stream) Tag() Tag {
	return s.tag
}

func (s *stream) SetTag(tag Tag) {
	s.tag = tag
}

func (s *stream) ID() ConnID {
	return s.id
}

func (s *stream) PeerVersion() uint16 {
	return s.peerVersion
}

func (s *stream) DialTimeout() time.Duration {
	return s.dialTimeout
}

func (s *stream) RoundtripLatency() time.Duration {
	return s.roundtripLatency
}

func (s *stream) SessionID() SessionID {
	return s.sessionID
}

func (s *stream) Receive(ctx context.Context) (Message, error) {
	if s.datagrams == nil {
		return s.receiveFromTransport(ctx)
	}
	// A datagram for this flow can arrive at any time, independently of the transport
	// stream, so the two must be waited on concurrently. A goroutine that pumps the
	// (blocking) transport receive into a channel lets this select do that; it is
	// started at most once and runs until the transport reports an error, i.e. for the
	// lifetime of the flow.
	s.pumpOnce.Do(func() {
		s.pumpCh = make(chan recvResult, 1)
		go s.pumpTransportRecv(ctx)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case b := <-s.datagrams:
		clog.Tracef(ctx, "<- %s %s, datagram len %d", s.tag, s.id, len(b))
		return NewMessage(Normal, b), nil
	case r := <-s.pumpCh:
		return r.m, r.err
	}
}

// pumpTransportRecv repeatedly calls receiveFromTransport and forwards every result to
// pumpCh, stopping after the first error (receiveFromTransport itself never succeeds
// again after that, so there is nothing more to pump).
func (s *stream) pumpTransportRecv(ctx context.Context) {
	for {
		m, err := s.receiveFromTransport(ctx)
		select {
		case s.pumpCh <- recvResult{m, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *stream) receiveFromTransport(ctx context.Context) (Message, error) {
	var cm *rpc.TunnelMessage
	var err error
	if streamCtx, ok := s.grpcStream.(GRPCContextStream); ok {
		cm, err = streamCtx.RecvContext(ctx)
	} else {
		cm, err = s.grpcStream.Recv()
	}
	if err != nil {
		return nil, err
	}
	m := msg(cm.Payload)
	switch m.Code() {
	case closeSend:
		clog.Tracef(ctx, "<- %s %s, close send", s.tag, s.id)
		// The conversation ends here, at the message level; the transport stream
		// will never be read again, so give transports that need it (QUIC) the
		// chance to release their receive direction.
		if rc, ok := s.grpcStream.(RecvCloser); ok {
			rc.CloseRecv()
		}
		return nil, net.ErrClosed
	case streamInfo:
		clog.Tracef(ctx, "<- %s, %s", s.tag, m)
	default:
		clog.Tracef(ctx, "<- %s %s, %s", s.tag, s.id, m)
	}
	return m, nil
}

func (s *stream) Send(ctx context.Context, m Message) error {
	// Only a UDP flow's Normal (payload) messages are datagram-eligible; everything
	// else -- streamInfo, DialOK/DialReject, Disconnect, KeepAlive, closeSend -- keeps
	// the stream's ordering and delivery guarantees.
	if m.Code() == Normal && s.id.Protocol() == types.ProtoUDP {
		if dc, ok := s.grpcStream.(DatagramCapable); ok && dc.SupportsDatagrams() {
			if err := dc.SendDatagram(EncodeDatagram(s.id, m.Payload())); err == nil {
				if s.datagramCounters != nil {
					s.datagramCounters.sent.Add(1)
				}
				clog.Tracef(ctx, "-> %s %s, datagram len %d", s.tag, s.id, len(m.Payload()))
				return nil
			} else if s.datagramCounters != nil {
				s.datagramCounters.fallback.Add(1)
			}
			// The datagram was rejected (typically DatagramTooLargeError) or the send
			// otherwise failed; fall back to the stream for this message only -- MTU
			// can change, so a fallback here must not latch.
		}
	}
	var err error
	if streamCtx, ok := s.grpcStream.(GRPCContextStream); ok {
		err = streamCtx.SendContext(ctx, m.TunnelMessage())
	} else {
		err = s.grpcStream.Send(m.TunnelMessage())
	}
	if err != nil {
		if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
			clog.Errorf(ctx, "!! %s %s, Send failed: %v", s.tag, s.id, err)
		}
		return err
	}
	clog.Tracef(ctx, "-> %s %s, %s", s.tag, s.id, m)
	return nil
}

func (s *stream) CloseSend(ctx context.Context) error {
	if err := s.Send(ctx, NewMessage(closeSend, nil)); err != nil {
		if ctx.Err() == nil && !(errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)) {
			return fmt.Errorf("send of closeSend message failed: %w", err)
		}
	}
	return nil
}

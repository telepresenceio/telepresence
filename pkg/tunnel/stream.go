package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// Version
//
//	0 which didn't report versions and didn't do synchronization
//	1 used MuxTunnel instead of one tunnel per connection.
const Version = uint16(2)

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
	Tag() string
	ID() ConnID
	Receive(context.Context) (Message, error)
	Send(context.Context, Message) error
	CloseSend(ctx context.Context) error
	PeerVersion() uint16
	SessionID() string
	DialTimeout() time.Duration
	RoundtripLatency() time.Duration
}

// StreamCreator is a function that creats a Stream.
type StreamCreator func(context.Context, ConnID) (Stream, error)

// ReadLoop reads from the Stream and dispatches messages and error to the give channels. There
// will be max one error since the error also terminates the loop.
func ReadLoop(ctx context.Context, s Stream, p *CounterProbe) (<-chan Message, <-chan error) {
	msgCh := make(chan Message, 50)
	errCh := make(chan error, 1) // Max one message will be sent on this channel
	dlog.Tracef(ctx, "   %s %s, ReadLoop starting", s.Tag(), s.ID())
	go func() {
		ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "ReadLoop")
		defer span.End()
		s.ID().SpanRecord(span)
		var endReason string
		defer func() {
			close(errCh)
			close(msgCh)
			dlog.Tracef(ctx, "   %s %s, ReadLoop ended: %s", s.Tag(), s.ID(), endReason)
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
			case errors.Is(err, context.Canceled), status.Code(err) == codes.Canceled:
				endReason = err.Error()
			default:
				endReason = err.Error()
				select {
				case errCh <- fmt.Errorf("!! %s %s, read from grpc.ClientStream failed: %w", s.Tag(), s.ID(), err):
				default:
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
	dlog.Tracef(ctx, "   %s %s, WriteLoop starting", s.Tag(), s.ID())
	go func() {
		ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "WriteLoop")
		defer span.End()
		s.ID().SpanRecord(span)
		var endReason string
		defer func() {
			dlog.Tracef(ctx, "   %s %s, WriteLoop ended: %s", s.Tag(), s.ID(), endReason)
			if err := s.CloseSend(ctx); err != nil {
				dlog.Errorf(ctx, "!! %s %s, Send of closeSend failed: %v", s.Tag(), s.ID(), err)
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
					dlog.Errorf(ctx, "!! %s %s, Send failed: %v", s.Tag(), s.ID(), err)
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
	sessionID        string
	tag              string
	syncRatio        uint32 // send and check sync after each syncRatio message
	ackWindow        uint32 // maximum permitted difference between sent and received ack
	peerVersion      uint16
}

func newStream(tag string, grpcStream GRPCStream) stream {
	return stream{tag: tag, grpcStream: grpcStream, syncRatio: 8, ackWindow: 1}
}

func (s *stream) Tag() string {
	return s.tag
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

func (s *stream) SessionID() string {
	return s.sessionID
}

func (s *stream) Receive(ctx context.Context) (Message, error) {
	cm, err := s.grpcStream.Recv()
	if err != nil {
		return nil, err
	}
	m := msg(cm.Payload)
	switch m.Code() {
	case closeSend:
		dlog.Tracef(ctx, "<- %s %s, close send", s.tag, s.id)
		return nil, net.ErrClosed
	case streamInfo:
		dlog.Tracef(ctx, "<- %s, %s", s.tag, m)
	default:
		dlog.Tracef(ctx, "<- %s %s, %s", s.tag, s.id, m)
	}
	return m, nil
}

func (s *stream) Send(ctx context.Context, m Message) error {
	if err := s.grpcStream.Send(m.TunnelMessage()); err != nil {
		if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
			dlog.Errorf(ctx, "!! %s %s, Send failed: %v", s.tag, s.id, err)
		}
		return err
	}
	dlog.Tracef(ctx, "-> %s %s, %s", s.tag, s.id, m)
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

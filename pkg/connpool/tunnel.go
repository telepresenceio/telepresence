package connpool

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type BidiStream interface {
	Send(*rpc.ConnMessage) error
	Recv() (*rpc.ConnMessage, error)
}

type Tunnel interface {
	DialLoop(ctx context.Context, closing *int32, pool *Pool) error
	ReadLoop(ctx context.Context, closing *int32) (<-chan Message, <-chan error)
	Send(Message) error
	Receive() (Message, error)
	CloseSend() error
}

type tunnel struct {
	stream BidiStream
}

func NewTunnel(stream BidiStream) Tunnel {
	return &tunnel{stream: stream}
}

func (s *tunnel) Receive() (Message, error) {
	cm, err := s.stream.Recv()
	if err != nil {
		return nil, err
	}
	return FromConnMessage(cm), nil
}

func (s *tunnel) Send(m Message) error {
	return s.stream.Send(m.TunnelMessage())
}

func (s *tunnel) CloseSend() error {
	if sender, ok := s.stream.(interface{ CloseSend() error }); ok {
		return sender.CloseSend()
	}
	return errors.New("tunnel does not implement CloseSend()")
}

// ReadLoop reads from the stream and dispatches control messages and messages to the give channels
func (s *tunnel) ReadLoop(ctx context.Context, closing *int32) (<-chan Message, <-chan error) {
	msgCh := make(chan Message)
	errCh := make(chan error)
	go func() {
		defer func() {
			close(errCh)
			close(msgCh)
		}()

		for atomic.LoadInt32(closing) == 0 {
			msg, err := s.Receive()
			if err != nil {
				if atomic.LoadInt32(closing) == 0 && ctx.Err() == nil {
					errCh <- client.WrapRecvErr(err, "read from grpc.ClientStream failed")
				}
				return
			}
			msgCh <- msg
		}
	}()
	return msgCh, errCh
}

// DialLoop reads replies from the stream and dispatches them to the correct connection
// based on the message id.
func (s *tunnel) DialLoop(ctx context.Context, closing *int32, pool *Pool) error {
	msgCh, errCh := s.ReadLoop(ctx, closing)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			return err
		case msg := <-msgCh:
			if msg == nil {
				return nil
			}
			if ctrl, ok := msg.(Control); ok {
				s.handleControl(ctx, ctrl, pool)
				continue
			}
			id := msg.ID()
			dlog.Debugf(ctx, "<- MGR %s, len %d", id.ReplyString(), len(msg.Payload()))
			if conn, _, _ := pool.Get(ctx, id, nil); conn != nil {
				conn.HandleMessage(ctx, msg)
			}
		}
	}
}

func (s *tunnel) handleControl(ctx context.Context, ctrl Control, pool *Pool) {
	id := ctrl.ID()

	dlog.Debugf(ctx, "<- MGR %s, code %s", id.ReplyString(), ctrl.Code())
	conn, _, err := pool.Get(ctx, id, func(ctx context.Context, release func()) (Handler, error) {
		if ctrl.Code() != Connect {
			// Only Connect requested from peer may create a new instance at this point
			return nil, nil
		}
		return NewDialer(id, s, release), nil
	})
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	if conn != nil {
		conn.HandleMessage(ctx, ctrl)
		return
	}
	if ctrl.Code() != ReadClosed && ctrl.Code() != DisconnectOK {
		dlog.Error(ctx, "control packet lost because no connection was active")
	}
}

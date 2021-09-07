package connpool

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
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
	Send(context.Context, Message) error
	Receive(context.Context) (Message, error)
	CloseSend() error
}

type tunnel struct {
	stream    BidiStream
	counter   uint32
	lastAck   uint32
	syncRatio uint32 // send and check sync after each syncRatio message
	ackWindow uint32 // maximum permitted difference between sent and received ack
}

func NewTunnel(stream BidiStream) Tunnel {
	return &tunnel{stream: stream, syncRatio: 8, ackWindow: 1}
}

func (s *tunnel) Receive(ctx context.Context) (msg Message, err error) {
	for err = ctx.Err(); err == nil; err = ctx.Err() {
		var cm *rpc.ConnMessage
		if cm, err = s.stream.Recv(); err != nil {
			return nil, err
		}
		msg = FromConnMessage(cm)
		if ctrl, ok := msg.(Control); ok {
			switch ctrl.Code() {
			case SyncRequest:
				if err = s.stream.Send(SyncResponseControl(ctrl).TunnelMessage()); err != nil {
					return nil, fmt.Errorf("failed to send sync response: %w", err)
				}
				continue
			case SyncResponse:
				atomic.StoreUint32(&s.lastAck, ctrl.AckNumber())
				continue
			}
		}
		return msg, nil
	}
	return nil, err
}

func (s *tunnel) Send(ctx context.Context, m Message) error {
	if err := s.stream.Send(m.TunnelMessage()); err != nil {
		return err
	}

	// Sync unless Control
	if _, ok := m.(Control); !ok {
		s.counter++
		if s.counter%s.syncRatio == 0 { // sync every nth package
			return s.sync(ctx)
		}
	}
	return nil
}

func (s *tunnel) sync(ctx context.Context) error {
	ackSent := s.counter / s.syncRatio
	if err := s.stream.Send(SyncRequestControl(ackSent).TunnelMessage()); err != nil {
		return err
	}
	for ctx.Err() == nil {
		lastAck := atomic.LoadUint32(&s.lastAck)
		if ackSent <= lastAck+s.ackWindow {
			dlog.Debugf(ctx, "Received Ack of %d", ackSent)
			break
		}
		dtime.SleepWithContext(ctx, time.Millisecond)
	}
	return nil
}

func (s *tunnel) CloseSend() error {
	if sender, ok := s.stream.(interface{ CloseSend() error }); ok {
		atomic.StoreUint32(&s.lastAck, math.MaxUint32) // Terminate ongoing sync
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
			msg, err := s.Receive(ctx)
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
			if conn := pool.Get(id); conn != nil {
				conn.HandleMessage(ctx, msg)
			}
		}
	}
}

func (s *tunnel) handleControl(ctx context.Context, ctrl Control, pool *Pool) {
	id := ctrl.ID()

	dlog.Debugf(ctx, "<- MGR %s, code %s", id.ReplyString(), ctrl.Code())
	conn, _, err := pool.GetOrCreate(ctx, id, func(ctx context.Context, release func()) (Handler, error) {
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

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

// The Tunnel interface represents a bidirectional, synchronized Tunnel that sends
// TCP or UDP traffic over gRPC using manager.ConnMessage messages.
//
// A Tunnel is closed by one of six things happening at either end (or at both ends).
//
//   1. Read from local connection fails (typically EOF)
//   2. Write to local connection fails (connection peer closed)
//   3. Idle timer timed out.
//   4. Context is cancelled.
//   5. Disconnect request received from Tunnel peer.
//   6. DisconnectOK received from Tunnel peer.
//
// When #1 or #2 happens, the Tunnel will send a Disconnect request to
// its Tunnel peer, shorten the Idle timer, and then continue to serve
// incoming data from the Tunnel peer until a DisconnectOK is received.
// Once that happens, it's guaranteed that the Tunnel peer will send no
// more messages and the Tunnel is closed.
//
// When #3, #4, or #5 happens, the Tunnel will send a DisconnectOK to its Tunnel peer and close.
//
// When #6 happens, the Tunnel will simply close.
type Tunnel interface {
	DialLoop(ctx context.Context, pool *Pool) error
	ReadLoop(ctx context.Context) (<-chan Message, <-chan error)
	Send(context.Context, Message) error
	Receive(context.Context) (Message, error)
	CloseSend() error
}

type tunnel struct {
	stream      BidiStream
	counter     uint32
	lastAck     uint32
	syncRatio   uint32 // send and check sync after each syncRatio message
	ackWindow   uint32 // maximum permitted difference between sent and received ack
	peerVersion uint32
}

const tunnelVersion = 1 // preceded by version 0 which didn't do synchronization

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
			case version:
				peerVersion := ctrl.version()
				atomic.StoreUint32(&s.peerVersion, uint32(peerVersion))
				dlog.Debugf(ctx, "setting tunnel's peer version to %d", peerVersion)
				continue
			case syncRequest:
				if err = s.stream.Send(SyncResponseControl(ctrl).TunnelMessage()); err != nil {
					return nil, fmt.Errorf("failed to send sync response: %w", err)
				}
				continue
			case syncResponse:
				atomic.StoreUint32(&s.lastAck, ctrl.ackNumber())
				continue
			}
		}
		return msg, nil
	}
	return nil, fmt.Errorf("tunnel receive canelled: %w", err)
}

func (s *tunnel) Send(ctx context.Context, m Message) error {
	if err := s.stream.Send(m.TunnelMessage()); err != nil {
		return err
	}
	if s.peerVersion < 1 {
		return nil // unable to synchronize
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
func (s *tunnel) ReadLoop(ctx context.Context) (<-chan Message, <-chan error) {
	msgCh := make(chan Message, 5)
	errCh := make(chan error)
	go func() {
		defer func() {
			close(errCh)
			close(msgCh)
		}()

		for ctx.Err() == nil {
			msg, err := s.Receive(ctx)
			if err != nil {
				if ctx.Err() == nil {
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
func (s *tunnel) DialLoop(ctx context.Context, pool *Pool) error {
	msgCh, errCh := s.ReadLoop(ctx)
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

	code := ctrl.Code()
	if len(id) == 0 {
		dlog.Errorf(ctx, "<- MGR <no id> message, code %s", code)
		return
	}
	dlog.Debugf(ctx, "<- MGR %s, code %s", id.ReplyString(), code)

	if code != Connect {
		if conn := pool.Get(id); conn != nil {
			conn.HandleMessage(ctx, ctrl)
		} else if !(code == Disconnect || code == DisconnectOK || code == ReadClosed || code == WriteClosed) {
			dlog.Errorf(ctx, "control packet of type %s lost because no connection was active", code)
		}
		return
	}

	conn, _, err := pool.GetOrCreate(ctx, id, func(ctx context.Context, release func()) (Handler, error) {
		return NewDialer(id, s, release), nil
	})
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	conn.HandleMessage(ctx, ctrl)
}

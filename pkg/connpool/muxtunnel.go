package connpool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type BidiStream interface {
	Send(*rpc.ConnMessage) error
	Recv() (*rpc.ConnMessage, error)
}

// The MuxTunnel interface represents a bidirectional, synchronized, multiplexed connection
// tunnel that sends TCP or UDP traffic over gRPC using manager.ConnMessage messages.
//
// A MuxTunnel connection is closed by one of six things happening at either end (or at both ends).
//
//   1. Read from local connection fails (typically EOF)
//   2. Write to local connection fails (connection peer closed)
//   3. Idle timer timed out.
//   4. Context is cancelled.
//   5. Disconnect request received from MuxTunnel peer.
//   6. DisconnectOK received from MuxTunnel peer.
//
// When #1 or #2 happens, the MuxTunnel will send a Disconnect request to
// its MuxTunnel peer, shorten the Idle timer, and then continue to serve
// incoming data from the tunnel peer until a DisconnectOK is received.
// Once that happens, it's guaranteed that the tunnel peer will send no
// more messages and the connection is closed.
//
// When #3, #4, or #5 happens, the MuxTunnel will send a DisconnectOK to
// its tunnel peer and close the connection.
//
// When #6 happens, the MuxTunnel will simply close.
type MuxTunnel interface {
	DialLoop(ctx context.Context, pool *tunnel.Pool) error
	ReadLoop(ctx context.Context) (<-chan Message, <-chan error)
	Send(context.Context, Message) error
	Receive(context.Context) (Message, error)
	CloseSend() error
	ReadPeerVersion(context.Context) (uint16, error)
}

type muxTunnel struct {
	stream      BidiStream
	counter     uint32
	lastAck     uint32
	syncRatio   uint32 // send and check sync after each syncRatio message
	ackWindow   uint32 // maximum permitted difference between sent and received ack
	peerVersion uint32
	pushBack    Message
}

func NewMuxTunnel(stream BidiStream) MuxTunnel {
	return &muxTunnel{stream: stream, syncRatio: 8, ackWindow: 1}
}

func (s *muxTunnel) ReadPeerVersion(ctx context.Context) (uint16, error) {
	cm, err := s.stream.Recv()
	if err != nil {
		return 0, err
	}
	msg := FromConnMessage(cm)
	if ctrl, ok := msg.(Control); ok && ctrl.Code() == version {
		peerVersion := ctrl.version()
		atomic.StoreUint32(&s.peerVersion, uint32(peerVersion))
		dlog.Debugf(ctx, "setting tunnel's peer version to %d", peerVersion)
		return peerVersion, nil
	}
	s.pushBack = msg
	return 0, nil
}

func (s *muxTunnel) Receive(ctx context.Context) (msg Message, err error) {
	if s.pushBack != nil {
		msg = s.pushBack
		s.pushBack = nil
		return msg, nil
	}

	for err = ctx.Err(); err == nil; err = ctx.Err() {
		var cm *rpc.ConnMessage
		if cm, err = s.stream.Recv(); err != nil {
			return nil, err
		}
		msg = FromConnMessage(cm)
		if ctrl, ok := msg.(Control); ok {
			switch ctrl.Code() {
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
	return nil, err
}

func (s *muxTunnel) Send(ctx context.Context, m Message) error {
	if err := s.stream.Send(m.TunnelMessage()); err != nil {
		return err
	}
	if s.peerVersion < 1 {
		return nil // unable to synchronize
	}

	// Sync unless Control
	if _, ok := m.(Control); !ok {
		s.counter++
		if s.counter%s.syncRatio == 0 { // sync every nth packet
			return s.sync(ctx)
		}
	}
	return nil
}

func (s *muxTunnel) sync(ctx context.Context) error {
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

func (s *muxTunnel) CloseSend() error {
	if sender, ok := s.stream.(interface{ CloseSend() error }); ok {
		atomic.StoreUint32(&s.lastAck, math.MaxUint32) // Terminate ongoing sync
		return sender.CloseSend()
	}
	return errors.New("tunnel does not implement CloseSend()")
}

// ReadLoop reads from the stream and dispatches control messages and messages to the give channels
func (s *muxTunnel) ReadLoop(ctx context.Context) (<-chan Message, <-chan error) {
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
				if ctx.Err() == nil && !(errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed)) {
					errCh <- fmt.Errorf("read from MuxTunnel failed: %w", err)
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
func (s *muxTunnel) DialLoop(ctx context.Context, pool *tunnel.Pool) error {
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
			if conn, ok := pool.Get(id).(Handler); ok {
				conn.HandleMessage(ctx, msg)
			}
		}
	}
}

func (s *muxTunnel) handleControl(ctx context.Context, ctrl Control, pool *tunnel.Pool) {
	id := ctrl.ID()

	code := ctrl.Code()
	if len(id) == 0 {
		dlog.Errorf(ctx, "<- MGR <no id> message, code %s", code)
		return
	}
	dlog.Debugf(ctx, "<- MGR %s, code %s", id.ReplyString(), code)

	if code != Connect {
		if conn, ok := pool.Get(id).(Handler); ok {
			conn.HandleMessage(ctx, ctrl)
		} else if !(code == Disconnect || code == DisconnectOK || code == ReadClosed || code == WriteClosed) {
			dlog.Errorf(ctx, "control packet of type %s lost because no connection was active", code)
		}
		return
	}

	bConn, _, err := pool.GetOrCreate(ctx, id, func(ctx context.Context, release func()) (tunnel.Handler, error) {
		return NewDialer(id, s, release), nil
	})
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	if conn, ok := bConn.(Handler); ok {
		conn.HandleMessage(ctx, ctrl)
	} else {
		dlog.Errorf(ctx, "Found handler of incorrect type. Expected conpool.Handler, got %T", bConn)
	}
}

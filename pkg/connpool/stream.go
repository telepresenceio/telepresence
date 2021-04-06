package connpool

import (
	"context"
	"fmt"
	"sync/atomic"

	"google.golang.org/grpc"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type Stream struct {
	grpc.ClientStream
	pool *Pool
}

func NewStream(bidiStream grpc.ClientStream, pool *Pool) *Stream {
	return &Stream{ClientStream: bidiStream, pool: pool}
}

// ReadLoop reads replies from the stream and dispatches them to the correct connection
// based on the message id.
func (s *Stream) ReadLoop(ctx context.Context, closing *int32) error {
	for atomic.LoadInt32(closing) == 0 {
		cm := new(manager.ConnMessage)
		err := s.RecvMsg(cm)
		if err != nil {
			if atomic.LoadInt32(closing) == 0 && ctx.Err() == nil {
				return fmt.Errorf("read from grpc.ClientStream failed: %s", err)
			}
			return nil
		}

		if IsControlMessage(cm) {
			ctrl, err := NewControlMessage(cm)
			if err != nil {
				dlog.Error(ctx, err)
				continue
			}

			dlog.Debugf(ctx, "<- MGR %s, code %s", ctrl.ID.ReplyString(), ctrl.Code)
			if conn, _ := s.pool.Get(ctx, ctrl.ID, nil); conn != nil {
				conn.HandleControl(ctx, ctrl)
			} else if ctrl.Code != ReadClosed && ctrl.Code != DisconnectOK {
				dlog.Error(ctx, "control packet lost because no connection was active")
			}
			continue
		}
		id := ConnID(cm.ConnId)
		dlog.Debugf(ctx, "<- MGR %s, len %d", id.ReplyString(), len(cm.Payload))
		if conn, _ := s.pool.Get(ctx, id, nil); conn != nil {
			conn.HandleMessage(ctx, cm)
		}
	}
	return nil
}

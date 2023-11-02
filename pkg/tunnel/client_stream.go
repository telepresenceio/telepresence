package tunnel

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type GRPCClientStream interface {
	GRPCStream
	CloseSend() error
}

func NewClientStream(ctx context.Context, grpcStream GRPCClientStream, id ConnID, sessionID string, callDelay, dialTimeout time.Duration) (Stream, error) {
	s := &clientStream{stream: newStream("CLI", grpcStream)}
	s.id = id
	s.roundtripLatency = callDelay
	s.dialTimeout = dialTimeout
	s.sessionID = sessionID

	if err := s.Send(ctx, StreamInfoMessage(id, sessionID, callDelay, dialTimeout)); err != nil {
		_ = s.CloseSend(ctx)
		return nil, err
	}
	m, err := s.Receive(ctx)
	if err != nil {
		_ = s.CloseSend(ctx)
		return nil, fmt.Errorf("failed to read initial StreamOK message: %w", err)
	}
	if m.Code() != streamOK {
		_ = s.CloseSend(ctx)
		return nil, errors.New("initial message was not StreamOK")
	}
	s.peerVersion = getVersion(m)
	return s, nil
}

type clientStream struct {
	stream
}

func (s *clientStream) CloseSend(_ context.Context) error {
	return s.grpcStream.(GRPCClientStream).CloseSend()
}

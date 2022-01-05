package tunnel

import (
	"context"
	"errors"
	"fmt"
)

func NewServerStream(ctx context.Context, grpcStream GRPCStream) (Stream, error) {
	s := &stream{tag: "SRV", grpcStream: grpcStream, syncRatio: 8, ackWindow: 1}
	m, err := s.Receive(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read initial StreamInfo message: %w", err)
	}
	if m.Code() != streamInfo {
		return nil, errors.New("initial message was not StreamInfo")
	}
	if err = setConnectInfo(m, s); err != nil {
		return nil, fmt.Errorf("failed to parse StreamInfo message: %w", err)
	}
	if err = s.Send(ctx, StreamOKMessage()); err != nil {
		return nil, err
	}
	return s, nil
}

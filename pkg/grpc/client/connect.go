package client

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

func DialGRPC(ctx context.Context, addr string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, err
	}

	state := conn.GetState()
	conn.Connect()

	// Wait for the connection to reach READY state or fail
	for {
		switch state {
		case connectivity.Ready:
			// Connection is established
			return conn, nil
		case connectivity.Shutdown:
			conn.Close()
			return nil, fmt.Errorf("connection failed: state=%v", state)
		default:
			if !conn.WaitForStateChange(ctx, state) {
				// Normal. The context timed out before the connection reached READY state.
				conn.Close()
				return nil, fmt.Errorf("%v: %w", state, ctx.Err())
			}
			state = conn.GetState()
		}
	}
}

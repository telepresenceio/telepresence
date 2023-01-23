package client

import (
	"context"
	"fmt"
	"io"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
)

type DaemonManager struct{}

func (d *DaemonManager) Client(ctx context.Context) (io.Closer, daemon.DaemonClient, error) {
	conn, err := DialSocket(ctx, DaemonSocketName,
		grpc.WithUnaryInterceptor(otelgrpc.UnaryClientInterceptor()),
		grpc.WithStreamInterceptor(otelgrpc.StreamClientInterceptor()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("unable open root daemon socket: %w", err)
	}

	return conn, daemon.NewDaemonClient(conn), nil
}

func (d *DaemonManager) IsRunning(ctx context.Context) (bool, error) {
	return IsRunning(ctx, DaemonSocketName)
}

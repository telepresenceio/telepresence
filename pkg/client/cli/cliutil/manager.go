package cliutil

import (
	"context"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func WithManager(ctx context.Context, fn func(context.Context, manager.ManagerClient) error) error {
	return WithConnector(ctx, func(ctx context.Context, _ connector.ConnectorClient) error {
		conn := ctx.Value(connectorConnCtxKey{}).(*grpc.ClientConn)
		managerClient := manager.NewManagerClient(conn)
		return fn(ctx, managerClient)
	})
}

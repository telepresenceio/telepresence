package cliutil

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func WithManager(ctx context.Context, fn func(context.Context, manager.ManagerClient) error) error {
	return WithConnector(ctx, func(ctx context.Context, _ connector.ConnectorClient) error {
		conn := getConnectorConn(ctx)
		managerClient := manager.NewManagerClient(conn)
		return fn(ctx, managerClient)
	})
}

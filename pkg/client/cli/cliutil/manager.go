package cliutil

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func WithManager(ctx context.Context, fn func(context.Context, manager.ManagerClient) error) error {
	userD := GetUserDaemon(ctx)
	managerClient := manager.NewManagerClient(userD.Conn)
	return fn(ctx, managerClient)
}

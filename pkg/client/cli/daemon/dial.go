package daemon

import (
	"context"
	"errors"

	"google.golang.org/grpc"
)

const InfoFileName = "daemon.json"

var ErrNoRootDaemon = errors.New("telepresence root daemon is not running")

func DialRootDaemon(ctx context.Context, waitForConnect bool) (conn *grpc.ClientConn, err error) {
	if ri, err := LoadRootServiceInfo(ctx); err == nil {
		return dialDaemon(ctx, "root", ri.DaemonPort)
	}
	return NewRootInfoLoader(ctx, false).DialDaemon(ctx, waitForConnect)
}

func DialUserDaemon(ctx context.Context, waitForConnect bool) (conn *grpc.ClientConn, err error) {
	return NewUserInfoLoader(ctx).DialDaemon(ctx, waitForConnect)
}

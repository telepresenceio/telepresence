package main

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	userDaemon "github.com/telepresenceio/telepresence/v2/pkg/client/userd/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
)

func main() {
	ctx := context.Background()
	if cli.IsDaemon() {
		ctx = userd.WithNewServiceFunc(ctx, userDaemon.NewService)
		ctx = userd.WithNewSessionFunc(ctx, trafficmgr.NewSession)
		ctx = rootd.WithNewServiceFunc(ctx, rootd.NewService)
		ctx = rootd.WithNewSessionFunc(ctx, rootd.NewSession)
	}
	cli.Main(ctx)
}

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/spf13/cobra"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

const processName = "pod-daemon"

type Args struct {
	// TODO
}

func main() {
	ctx := context.Background()
	ctx = log.MakeBaseLogger(ctx, os.Getenv("LOG_LEVEL"))
	ctx = dgroup.WithGoroutineName(ctx, "/"+processName)

	var args Args
	cmd := &cobra.Command{
		Use:  "podd",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return Main(cmd.Context(), args)
		},
	}
	// TODO: Use flags to populate 'args':
	//
	//cmd.Flags().WhateverVar(&args.Whatever, "flagname", defaultVal,
	//	"description")

	if err := cmd.ExecuteContext(ctx); err != nil {
		dlog.Errorf(ctx, "quit: %v", err)
		os.Exit(1)
	}
}

// Main in mostly mimics pkg/client/userd.run(), but is trimmed down for running in a Pod.
func Main(ctx context.Context, args Args) error {
	cfg, err := client.LoadConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	ctx = client.WithConfig(ctx, cfg)

	scoutReporter := scout.NewReporter(ctx, processName)

	userdCoreImpl := &userd.Service{
		// TODO
	}

	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{
		SoftShutdownTimeout:  2 * time.Second,
		EnableSignalHandling: true,
		ShutdownOnNonError:   true,
	})

	grp.Go("telemetry", scoutReporter.Run)
	grp.Go("session", func(ctx context.Context) error {
		return userdCoreImpl.ManageSessions(ctx, []trafficmgr.SessionService{})
	})
	grp.Go("main", func(ctx context.Context) error {
		_, err := userdCoreImpl.Connect(ctx, &rpc.ConnectRequest{
			// TODO
		})
		if err != nil {
			return err
		}

		_, err = userdCoreImpl.CreateIntercept(ctx, &rpc.CreateInterceptRequest{
			// TODO
		})
		if err != nil {
			return err
		}

		// now just wait to be signaled to shut down
		<-ctx.Done()
		return nil
	})

	return grp.Wait()
}

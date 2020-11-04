package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/datawire/ambassador/pkg/dlog"
	"golang.org/x/sync/errgroup"

	"github.com/datawire/telepresence2/pkg/version"
)

func agent_main() {
	// Set up context with logger
	dlog.SetFallbackLogger(makeBaseLogger())
	g, ctx := errgroup.WithContext(dlog.WithField(context.Background(), "MAIN", "main"))

	if version.Version == "" {
		version.Version = "(devel)"
	}

	dlog.Infof(ctx, "Traffic Agent %s [pid:%d]", version.Version, os.Getpid())

	// Handle shutdown
	g.Go(func() error {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

		select {
		case sig := <-sigs:
			dlog.Errorf(ctx, "Shutting down due to signal %v", sig)
			return fmt.Errorf("received signal %v", sig)
		case <-ctx.Done():
			return nil
		}
	})

	// Wait for exit
	if err := g.Wait(); err != nil {
		dlog.Errorf(ctx, "quit: %v", err)
		os.Exit(1)
	}
}

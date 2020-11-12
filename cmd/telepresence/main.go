package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/datawire/telepresence2/pkg/client/cli"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	if err := cli.Command().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

package sigctx

import (
	"context"
	"os"
	"os/signal"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// DoWithSignalHandler runs f with a context canceled when an INTERRUPT or TERMINATE signal is received.
func DoWithSignalHandler(ctx context.Context, f func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	sigs := make(chan os.Signal, 1)

	go func() {
		select {
		case sig := <-sigs:
			clog.Infof(ctx, "Received %s, shutting down", sig)
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(sigs)
		close(sigs)
	}()
	signal.Notify(sigs, proc.SignalsToForward...)
	return f(ctx)
}

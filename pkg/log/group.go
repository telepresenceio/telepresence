package log

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/telepresenceio/clog"
)

// Group is a wrapper around [errgroup.Group] and a [context.Context].
type group struct {
	context.Context
	*errgroup.Group
}

// Group is a wrapper around [errgroup.Group] and a [context.Context]. It changes the semantics of the Go method to use the given
// name as a [slog.Logger] group prefix.
type Group interface {
	context.Context

	// Go runs the given function in a goroutine where the context logger uses the given name as a group prefix.
	Go(name string, f func(context.Context) error)

	// Wait blocks until all function calls from the Go method have returned, then returns the first non-nil error (if any) from them.
	Wait() error
}

// NewGroup returns a new Group with the given context.
func NewGroup(ctx context.Context) Group {
	g := group{}
	g.Group, g.Context = errgroup.WithContext(ctx)
	return g
}

// Go runs the given function in a goroutine where the context logger uses the given name as a group prefix.
func (g group) Go(name string, f func(context.Context) error) {
	g.Group.Go(func() (err error) {
		ctx := clog.WithGroup(g.Context, name)
		started := time.Now()
		defer func() {
			elapsed := time.Since(started).Round(time.Millisecond)
			ctxErr := ctx.Err()
			cause := context.Cause(ctx)
			switch {
			case err != nil:
				clog.Errorf(ctx, "service goroutine ended after %s: err=%v context=%v cause=%v", elapsed, err, ctxErr, cause)
			case ctxErr != nil:
				clog.Debugf(ctx, "service goroutine stopped after %s: context=%v cause=%v", elapsed, ctxErr, cause)
			default:
				clog.Debugf(ctx, "service goroutine completed after %s", elapsed)
			}
		}()
		return f(ctx)
	})
}

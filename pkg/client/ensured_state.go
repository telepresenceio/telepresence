package client

import (
	"context"
	"fmt"
	"time"

	"github.com/datawire/dlib/dcontext"
)

type (
	Prolog[T any] func(T, context.Context) (acquired bool, err error)
	Action[T any] func(T, context.Context) error
)

// WithEnsuredState calls prolog, and if that was successful, calls act. If epilog is not nil, it is guaranteed
// to be called when prolog returns true.
func WithEnsuredState[T any](ctx context.Context, r T, prolog Prolog[T], action, epilog Action[T]) error {
	wasAcquired, err := prolog(r, ctx)
	if err != nil {
		return err
	}
	if wasAcquired && epilog != nil {
		defer func() {
			// The context might have been cancelled, so we use the original context
			// without cancellation, but with a deactivation timeout of 10 seconds.
			ctx, cancel := context.WithTimeout(dcontext.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if cerr := epilog(r, ctx); cerr != nil {
				if err == nil {
					err = cerr
				} else {
					err = fmt.Errorf("%w\n%v", err, cerr)
				}
			}
		}()
	}
	if action != nil {
		err = action(r, ctx)
	}
	return err
}

package client

import (
	"context"
	"fmt"
	"time"
)

type (
	Prolog func(context.Context) (acquired bool, err error)
	Action func(context.Context) error
)

// WithEnsuredState calls prolog, and if that was successful, calls act. If epilog is not nil, it is guaranteed
// to be called when prolog returns true.
func WithEnsuredState(ctx context.Context, prolog Prolog, action, epilog Action) error {
	wasAcquired, err := prolog(ctx)
	if err != nil {
		return err
	}
	if wasAcquired && epilog != nil {
		defer func() {
			// The context might have been cancelled, so we use the original context
			// without cancellation, but with a deactivation timeout of 10 seconds.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if cerr := epilog(ctx); cerr != nil {
				if err == nil {
					err = cerr
				} else {
					err = fmt.Errorf("%w\n%v", err, cerr)
				}
			}
		}()
	}
	if action != nil {
		err = action(ctx)
	}
	return err
}

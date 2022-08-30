package client

import (
	"context"
	"fmt"
	"time"

	"github.com/datawire/dlib/dcontext"
)

// An EnsuredState represents some state that is needed in order for a function to execute.
type EnsuredState interface {
	// EnsureState will check if the state is active and activate it if that is not the case.
	// The boolean return value indicates if the state was activated or not.
	EnsureState(ctx context.Context) (bool, error)

	// DeactivateState performs actions such as quit, remove, or disconnect
	DeactivateState(ctx context.Context) error
}

// WithEnsuredState ensures the given state, calls the function, and then, if the state
// was activated, it is deactivated unless the retain flag is true.
func WithEnsuredState(ctx context.Context, r EnsuredState, retain bool, f func() error) (err error) {
	var wasAcquired bool
	defer func() {
		// Always deactivate an acquired state unless there's no error
		// and a desire to retain it.
		if wasAcquired && (err != nil || !retain) {
			// The state might be deactivated because the context has been cancelled, so we cannot
			// call DeactivateState with an unmodified context here, so we use the original
			// context without cancellation, but with a deactivation timeout of 10 seconds
			ctx, cancel := context.WithTimeout(dcontext.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if cerr := r.DeactivateState(ctx); cerr != nil {
				if err == nil {
					err = cerr
				} else {
					err = fmt.Errorf("%w\n%v", err, cerr)
				}
			}
		}
	}()

	if wasAcquired, err = r.EnsureState(ctx); err != nil {
		return err
	}
	return f()
}

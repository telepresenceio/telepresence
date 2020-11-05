package client

import "fmt"

// An EnsuredState represents some state that is needed in order for a function to execute.
type EnsuredState interface {
	// EnsureState will check if the state is active and activate it if that is not the case.
	// The boolean return value indicates if the state was activated or not.
	EnsureState() (bool, error)

	// Deactivate the state (i.e. quit, remove, disconnect)
	DeactivateState() error
}

// WithEnsuredState ensures the given state, calls the function, and then, if the state
// was activated, it is deactivated.
func WithEnsuredState(r EnsuredState, f func() error) (err error) {
	var wasAcquired bool
	wasAcquired, err = r.EnsureState()
	if wasAcquired {
		defer func() {
			if cerr := r.DeactivateState(); cerr != nil {
				if err == nil {
					err = cerr
				} else {
					err = fmt.Errorf("%s\n%s", err.Error(), cerr.Error())
				}
			}
		}()
	}
	if err != nil {
		return err
	}
	err = f()
	return
}

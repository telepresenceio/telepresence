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
// was activated, it is deactivated unless the retain flag is true.
func WithEnsuredState(r EnsuredState, retain bool, f func() error) (err error) {
	var wasAcquired bool
	defer func() {
		// Always deactivate an acquired state unless there's no error
		// and a desire to retain it.
		if wasAcquired && (err != nil || !retain) {
			if cerr := r.DeactivateState(); cerr != nil {
				if err == nil {
					err = cerr
				} else {
					err = fmt.Errorf("%v\n%v", err, cerr)
				}
			}
		}
	}()

	if wasAcquired, err = r.EnsureState(); err != nil {
		return err
	}
	return f()
}

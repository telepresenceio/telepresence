package v25uninstall

import (
	"errors"
	"fmt"

	"github.com/hashicorp/go-multierror"
)

// alreadyUndoneError means that an install action has already been undone, perhaps by manual user action.
type alreadyUndoneError struct {
	err error
	msg string
}

func (e *alreadyUndoneError) Error() string {
	return fmt.Sprintf("%s: %v", e.msg, e.err)
}

func (e *alreadyUndoneError) Unwrap() error {
	return e.err
}

func newAlreadyUndone(err error, msg string) error {
	return &alreadyUndoneError{err, msg}
}

// isAlreadyUndone returns whether the given error -- possibly a multierror -- indicates that all actions have been undone.
func isAlreadyUndone(err error) bool {
	var undone *alreadyUndoneError
	if errors.As(err, &undone) {
		return true
	}
	var multi *multierror.Error
	if !errors.As(err, &multi) {
		return false
	}
	for _, err := range multi.WrappedErrors() {
		if !errors.As(err, &undone) {
			return false
		}
	}
	return true
}

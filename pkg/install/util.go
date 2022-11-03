package install

import (
	"errors"
	"fmt"

	"github.com/hashicorp/go-multierror"
)

// AlreadyUndoneError means that an install action has already been undone, perhaps by manual user action.
type AlreadyUndoneError struct {
	err error
	msg string
}

func (e *AlreadyUndoneError) Error() string {
	return fmt.Sprintf("%s: %v", e.msg, e.err)
}

func (e *AlreadyUndoneError) Unwrap() error {
	return e.err
}

func NewAlreadyUndone(err error, msg string) error {
	return &AlreadyUndoneError{err, msg}
}

// IsAlreadyUndone returns whether the given error -- possibly a multierror -- indicates that all actions have been undone.
func IsAlreadyUndone(err error) bool {
	var undone *AlreadyUndoneError
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

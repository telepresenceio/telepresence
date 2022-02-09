package install

import (
	"errors"
	"fmt"

	"github.com/hashicorp/go-multierror"
)

// AlreadyUndone means that an install action has already been undone, perhaps by manual user action
type AlreadyUndone struct {
	err error
	msg string
}

func (e *AlreadyUndone) Error() string {
	return fmt.Sprintf("%s: %v", e.msg, e.err)
}

func (e *AlreadyUndone) Unwrap() error {
	return e.err
}

func NewAlreadyUndone(err error, msg string) error {
	return &AlreadyUndone{err, msg}
}

// IsAlreadyUndone returns whether the given error -- possibly a multierror -- indicates that all actions have been undone.
func IsAlreadyUndone(err error) bool {
	var undone *AlreadyUndone
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

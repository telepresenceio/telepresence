package install

import (
	"errors"
	"fmt"
	"strconv"

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

const maxPortNameLen = 15

// HiddenPortName prefixes the given name with "tm-" and truncates it to 15 characters. If
// the ordinal is greater than zero, the last two digits are reserved for the hexadecimal
// representation of that ordinal.
func HiddenPortName(name string, ordinal int) string {
	// New name must be max 15 characters long
	hiddenName := "tm-" + name
	if len(hiddenName) > maxPortNameLen {
		if ordinal > 0 {
			hiddenName = hiddenName[:maxPortNameLen-2] + strconv.FormatInt(int64(ordinal), 16) // we don't expect more than 256 ports
		} else {
			hiddenName = hiddenName[:maxPortNameLen]
		}
	}
	return hiddenName
}

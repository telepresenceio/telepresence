package errcat

import (
	"errors"
	"fmt"
)

// The Category is used for categorizing errors so that we can know when
// to point the user to the logs or not.
type Category int

type categorized struct {
	error
	category Category
}

const (
	OK       = Category(iota)
	User     // User made an error
	Config   // Errors in config.yml, extensions, or kubeconfig
	OtherCLI // Error generated in the CLI process, so no use pointing logs
	Unknown  // Something else. Consult the logs
)

// New creates a new categorized error based in its argument. The argument
// can be an error or a string. If it isn't, it will be converted to a string
// using its '%v' formatter.
func (c Category) New(untypedErr interface{}) error {
	var err error
	switch untypedErr := untypedErr.(type) {
	case nil:
		return nil
	case error:
		err = untypedErr
	case string:
		err = errors.New(untypedErr)
	default:
		err = fmt.Errorf("%v", untypedErr)
	}
	return &categorized{error: err, category: c}
}

// Newf creates a new categorized error based on a format string with arguments. The
// error is created using fmt.Errorf() so using '%w' is relevant for error arguments.
func (c Category) Newf(format string, a ...interface{}) error {
	return &categorized{error: fmt.Errorf(format, a...), category: c}
}

// Unwrap this categorized error.
func (ce *categorized) Unwrap() error {
	return ce.error
}

// GetCategory returns the error category for a categorized error, OK for nil, and
// Unknown for other errors
func GetCategory(err error) Category {
	if err == nil {
		return OK
	}
	for {
		if ce, ok := err.(*categorized); ok {
			return ce.category
		}
		if err = errors.Unwrap(err); err == nil {
			return Unknown
		}
	}
}

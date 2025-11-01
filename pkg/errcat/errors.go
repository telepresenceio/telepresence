package errcat

import (
	"errors"
	"fmt"
	"os"

	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

// The Category is used for categorizing errors so that we can know when
// to point the user to the logs or not.
type Category int

type categorized struct {
	error
	category Category
}

type Categorized interface {
	error

	// GetCategory returns the error category for a categorized error.
	GetCategory() Category
}

const (
	OK           = Category(iota)
	User         // User made an error
	Config       // Errors in config.yml, extensions, or kubeconfig
	NoDaemonLogs // Other error generated in the CLI process, so no use pointing the user to logs
	Silent       // Don't print error on exit, it has already been conveyed to the user
	Unknown      // Something else. Consult the logs
)

// New creates a new categorized error based in its argument. The argument
// can be an error or a string. If it isn't, it will be converted to a string
// using its '%v' formatter.
func (c Category) New(untypedErr any) error {
	var err error
	switch untypedErr := untypedErr.(type) {
	case nil:
		return nil
	case *categorized:
		return untypedErr
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
func (c Category) Newf(format string, a ...any) error {
	return &categorized{error: fmt.Errorf(format, a...), category: c}
}

// Errorf creates a new categorized error based on a format string with arguments. The
// format string will get ": %w" appended to it, and the given error will be appended
// to the args before the returned error is created using fmt.Errorf().
func (c Category) Errorf(err error, format string, a ...any) error {
	return &categorized{error: fmt.Errorf(format+": %w", append(a, err)...), category: c}
}

// Print prints error on dos.Stderr(ctx) unless it is nil or Silent.
func Print(err error) {
	switch GetCategory(err) {
	case OK, Silent:
	default:
		ioutil.Println(os.Stderr, err.Error())
	}
}

// GetCategory returns the error category for a categorized error.
func (ce *categorized) GetCategory() Category {
	return ce.category
}

// Unwrap this categorized error.
func (ce *categorized) Unwrap() error {
	return ce.error
}

// GetCategory returns the error category for a categorized error, OK for nil, and
// Unknown for other errors.
func GetCategory(err error) Category {
	if err == nil {
		return OK
	}
	// Keep unwrapping until a category is found (or not)
	for {
		var ce Categorized
		if errors.As(err, &ce) {
			return ce.GetCategory()
		}
		if err = errors.Unwrap(err); err == nil {
			return Unknown
		}
	}
}

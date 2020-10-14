// +build windows

package teleproxy

import (
	"github.com/pkg/errors"
)

// RunAsIntercept is the main function when executing as
// teleproxy intercept
func RunAsIntercept(_, _ string) error {
	return errors.New("Not implemented on this platform")
}

// RunAsBridge is the main function when executing as
// teleproxy bridge
func RunAsBridge(_, _ string) error {
	return errors.New("Not implemented on this platform")
}

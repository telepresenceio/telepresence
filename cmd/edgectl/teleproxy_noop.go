// +build windows

package main

import (
	"github.com/pkg/errors"
)

// RunAsTeleproxyIntercept is the main function when executing as
// teleproxy intercept
func RunAsTeleproxyIntercept(_, _ string) error {
	return errors.New("Not implemented on this platform")
}

// RunAsTeleproxyBridge is the main function when executing as
// teleproxy bridge
func RunAsTeleproxyBridge(_, _ string) error {
	return errors.New("Not implemented on this platform")
}

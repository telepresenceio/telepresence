// +build windows

package connector

import (
	"github.com/pkg/errors"
)

// getFreePort is not implemented on this platform
func getFreePort() (int, error) {
	return 0, errors.New("Not implemented on this platform")
}

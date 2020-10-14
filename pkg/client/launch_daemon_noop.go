// +build windows

package client

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func LaunchDaemon(_ *cobra.Command, _ []string) error {
	return errors.New("Not implemented on this platform")
}

// DaemonWorks returns whether the daemon can function on this platform
func DaemonWorks() bool {
	return false
}

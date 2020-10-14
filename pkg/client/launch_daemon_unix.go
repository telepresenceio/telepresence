// +build !windows

package client

import (
	"fmt"
	"os"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

// LaunchDaemon will launch the daemon responsible for doing the network overrides. Only the root
// user can do this.
func LaunchDaemon(cmd *cobra.Command, args []string) error {
	if os.Geteuid() != 0 {
		// TODO: Attempt a sudo instead of reporting error
		return fmt.Errorf(`Edge Control Daemon must be launched as root.

 sudo %s
`, cmd.CommandPath())
	}
	dns, _ := cmd.Flags().GetString("dns")
	fallback, _ := cmd.Flags().GetString("fallback")
	ds, err := newDaemonState(cmd.OutOrStdout(), dns, fallback)
	defer ds.disconnect()
	if err == nil {
		return errors.New("Daemon already started")
	}
	_, err = ds.EnsureState()
	return err
}

// DaemonWorks returns whether the daemon can function on this platform
func DaemonWorks() bool {
	return true
}

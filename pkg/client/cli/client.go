package cli

import (
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

// IsServerRunning reports whether or not the daemon server is running.
func IsServerRunning() bool {
	return assertDaemonStarted() == nil
}

var errDaemonIsNotRunning = errors.New("the telepresence daemon has not been started")
var errConnectorIsNotRunning = errors.New("not connected")

// quit sends the quit message to the daemon and waits for it to exit.
func quit(cmd *cobra.Command, _ []string) error {
	si := &sessionInfo{cmd: cmd}
	ds, err := si.newDaemonState()
	if err == nil {
		// Let daemon kill the connector
		defer ds.disconnect()
		return ds.DeactivateState()
	}

	// Ensure the connector is killed even if daemon isn't running
	if err := cliutil.QuitConnector(cmd.Context()); err != nil {
		return err
	}

	return nil
}

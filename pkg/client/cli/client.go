package cli

import (
	"fmt"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// IsServerRunning reports whether or not the daemon server is running.
func IsServerRunning() bool {
	return assertDaemonStarted() == nil
}

var errDaemonIsNotRunning = errors.New("the telepresence daemon has not been started")
var errConnectorIsNotRunning = errors.New("not connected")

// printVersion requests version info from the daemon and prints both client and daemon version.
func printVersion(cmd *cobra.Command, _ []string) error {
	av, dv, err := daemonVersion(cmd)
	if err == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Client %s\nDaemon %s (api v%d)\n", client.DisplayVersion(), dv, av)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Client %s\n", client.DisplayVersion())
	if err == errDaemonIsNotRunning {
		err = nil
	}
	return err
}

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
	err = assertConnectorStarted()
	if err != nil {
		return nil
	}
	return si.withConnector(true, func(cs *connectorState) error {
		defer cs.disconnect()
		return cs.DeactivateState()
	})
}

func daemonVersion(cmd *cobra.Command) (apiVersion int, version string, err error) {
	err = withStartedDaemon(cmd, func(d *daemonState) error {
		vi, err := d.grpc.Version(cmd.Context(), &empty.Empty{})
		if err == nil {
			apiVersion = int(vi.ApiVersion)
			version = vi.Version
		}
		return err
	})
	return
}

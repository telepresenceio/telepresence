package cli

import (
	"fmt"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
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

// status will retrieve connectivity status from the daemon and print it on stdout.
func status(cmd *cobra.Command, _ []string) error {
	var ds *daemon.DaemonStatus
	var err error
	if ds, err = daemonStatus(cmd); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	switch ds.Error {
	case daemon.DaemonStatus_NOT_STARTED:
		fmt.Fprintln(out, "The telepresence daemon has not been started")
		return nil
	case daemon.DaemonStatus_NO_NETWORK:
		fmt.Fprintln(out, "Network overrides NOT established")
		return nil
	}

	if ds.Dns != "" {
		fmt.Fprintf(out, "DNS = %s\n", ds.Dns)
	}
	if ds.Fallback != "" {
		fmt.Fprintf(out, "Fallback = %s\n", ds.Fallback)
	}
	var cs *connector.ConnectInfo
	if cs, err = connectorStatus(cmd); err != nil {
		return err
	}
	switch cs.Error {
	case connector.ConnectInfo_UNSPECIFIED, connector.ConnectInfo_ALREADY_CONNECTED:
		if cs.ClusterOk {
			fmt.Fprintln(out, "Connected")
		} else {
			fmt.Fprintln(out, "Attempting to reconnect...")
		}
		fmt.Fprintf(out, "  Context:       %s (%s)\n", cs.ClusterContext, cs.ClusterServer)
		if cs.BridgeOk {
			fmt.Fprintln(out, "  Proxy:         ON (networking to the cluster is enabled)")
		} else {
			fmt.Fprintln(out, "  Proxy:         OFF (attempting to connect...)")
		}
		if cs.ErrorText != "" {
			fmt.Fprintf(out, "  Intercepts:    %s\n", cs.ErrorText)
		} else {
			ic := cs.Intercepts
			if ic == nil {
				fmt.Fprintln(out, "  Intercepts:    Unavailable: no traffic manager")
			} else {
				fmt.Fprintf(out, "  Intercepts:    %d total\n", len(ic.Intercepts))
				for _, ic := range ic.Intercepts {
					fmt.Fprintf(out, "    %s: %s\n", ic.Spec.Name, ic.Spec.Client)
				}
			}
		}
	case connector.ConnectInfo_NOT_STARTED:
		fmt.Fprintln(out, errConnectorIsNotRunning)
	case connector.ConnectInfo_DISCONNECTING:
		fmt.Fprintln(out, "Disconnecting")
	}
	return nil
}

func daemonStatus(cmd *cobra.Command) (status *daemon.DaemonStatus, err error) {
	err = withStartedDaemon(cmd, func(ds *daemonState) error {
		status, err = ds.grpc.Status(cmd.Context(), &empty.Empty{})
		return err
	})
	if err == errDaemonIsNotRunning {
		err = nil
		status = &daemon.DaemonStatus{Error: daemon.DaemonStatus_NOT_STARTED}
	}
	return
}

func connectorStatus(cmd *cobra.Command) (status *connector.ConnectInfo, err error) {
	err = withStartedConnector(cmd, func(cs *connectorState) error {
		status = cs.info
		return nil
	})
	if err == errConnectorIsNotRunning {
		err = nil
		status = &connector.ConnectInfo{Error: connector.ConnectInfo_NOT_STARTED}
	}
	return
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

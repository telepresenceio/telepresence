package cli

import (
	"fmt"
	"strings"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/rpc/connector"
	"github.com/datawire/telepresence2/pkg/rpc/daemon"
	"github.com/datawire/telepresence2/pkg/rpc/manager"
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
	case daemon.DaemonStatus_PAUSED:
		fmt.Fprintln(out, "Network overrides are paused")
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
	if assertDaemonStarted() != nil {
		return &daemon.DaemonStatus{Error: daemon.DaemonStatus_NOT_STARTED}, nil
	}
	err = withDaemon(cmd, func(d daemon.DaemonClient) error {
		status, err = d.Status(cmd.Context(), &empty.Empty{})
		return err
	})
	return
}

func connectorStatus(cmd *cobra.Command) (status *connector.ConnectInfo, err error) {
	if assertConnectorStarted() != nil {
		return &connector.ConnectInfo{Error: connector.ConnectInfo_NOT_STARTED}, nil
	}
	err = withConnector(cmd, func(cs *connectorState) error {
		status = cs.info
		return nil
	})
	return
}

// quit sends the quit message to the daemon and waits for it to exit.
func quit(cmd *cobra.Command, _ []string) error {
	ds, err := newDaemonState(cmd, "", "")
	if err == nil {
		// Let daemon kill the connector
		defer ds.disconnect()
		return ds.DeactivateState()
	}

	// Ensure the connector is killed even if daemon isn't running
	cs, err := newConnectorState(nil, nil, cmd)
	if err != nil {
		return nil
	}
	defer cs.disconnect()
	return cs.DeactivateState()
}

// listIntercepts requests a list current intercepts from the daemon
func listIntercepts(cmd *cobra.Command, _ []string) error {
	var r *manager.InterceptInfoSnapshot
	var err error
	err = withConnector(cmd, func(cs *connectorState) error {
		r, err = cs.grpc.ListIntercepts(cmd.Context(), &empty.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	stdout := cmd.OutOrStdout()
	if len(r.Intercepts) == 0 {
		fmt.Fprintln(stdout, "No intercepts")
		return nil
	}
	var previewURL string
	for idx, cept := range r.Intercepts {
		spec := cept.Spec
		fmt.Fprintf(stdout, "%4d. %s\n", idx+1, spec.Name)
		fmt.Fprintf(stdout, "      Intercepting requests and redirecting them to %s:%d\n", spec.TargetHost, spec.TargetPort)
	}
	if previewURL != "" {
		fmt.Fprintln(stdout, "Share a preview of your changes with anyone by visiting\n  ", previewURL)
	}
	return nil
}

// removeIntercept tells the daemon to deactivate and remove an existent intercept
func removeIntercept(cmd *cobra.Command, args []string) error {
	return withConnector(cmd, func(cs *connectorState) error {
		is := newInterceptState(cs,
			&manager.CreateInterceptRequest{InterceptSpec: &manager.InterceptSpec{Name: strings.TrimSpace(args[0])}},
			cmd)
		return is.DeactivateState()
	})
}

func daemonVersion(cmd *cobra.Command) (apiVersion int, version string, err error) {
	err = withDaemon(cmd, func(d daemon.DaemonClient) error {
		vi, err := d.Version(cmd.Context(), &empty.Empty{})
		if err == nil {
			apiVersion = int(vi.ApiVersion)
			version = vi.Version
		}
		return err
	})
	return
}

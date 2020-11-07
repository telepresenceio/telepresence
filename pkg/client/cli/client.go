package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
	manager "github.com/datawire/telepresence2/pkg/rpc"
	"github.com/datawire/telepresence2/pkg/rpc/connector"
	"github.com/datawire/telepresence2/pkg/rpc/daemon"
)

// IsServerRunning reports whether or not the daemon server is running.
func IsServerRunning() bool {
	return assertDaemonStarted() == nil
}

var daemonIsNotRunning = errors.New("The telepresence daemon has not been started.\nUse 'telepresence [--no-wait]' to start it.")
var connectorIsNotRunning = errors.New("Not connected (use 'telepresence [--no-wait]' to connect to your cluster)")

// printVersion requests version info from the daemon and prints both client and daemon version.
func printVersion(cmd *cobra.Command, _ []string) error {
	av, dv, err := daemonVersion(cmd)
	if err == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Client %s\nDaemon %s (api v%d)\n", client.DisplayVersion(), dv, av)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Client %s\n", client.DisplayVersion())
	if err != daemonIsNotRunning {
		// Socket exists but connection failed anyway.
		err = fmt.Errorf("Unable to connect to daemon: %s", err)
	}
	return err
}

// Connect asks the daemon to connect to a cluster
func (p *runner) connect(cmd *cobra.Command, args []string) error {
	ds, cs, err := p.ensureConnected(cmd)
	if err != nil {
		return err
	}
	cs.disconnect()
	ds.disconnect()
	return nil
}

func (p *runner) ensureConnected(cmd *cobra.Command) (*daemonState, *connectorState, error) {
	ds, err := newDaemonState(cmd, "", "")
	if err != nil && err != daemonIsNotRunning {
		return nil, nil, err
	}

	if err == daemonIsNotRunning {
		if _, err = ds.EnsureState(); err != nil {
			ds.disconnect()
			return nil, nil, err
		}
	}

	// When set, require a traffic manager and wait until it is connected
	p.InterceptEnabled = false

	cs, err := newConnectorState(ds.grpc, &p.ConnectRequest, cmd)
	if err != nil && err != connectorIsNotRunning {
		ds.disconnect()
		return nil, nil, err
	}

	if err == connectorIsNotRunning {
		if _, err = cs.EnsureState(); err != nil {
			ds.disconnect()
			cs.disconnect()
			return nil, nil, err
		}
	}
	return ds, cs, nil
}

// Disconnect asks the daemon to disconnect from the connected cluster
func Disconnect(cmd *cobra.Command, _ []string) error {
	cs, err := newConnectorState(nil, nil, cmd)
	if err != nil {
		return err
	}
	return cs.DeactivateState()
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
		fmt.Fprintln(out, daemonIsNotRunning)
		return nil
	case daemon.DaemonStatus_PAUSED:
		fmt.Fprintln(out, "Network overrides are paused")
		return nil
	case daemon.DaemonStatus_NO_NETWORK:
		fmt.Fprintln(out, "Network overrides NOT established")
		return nil
	}

	var cs *connector.ConnectorStatus
	if cs, err = connectorStatus(cmd); err != nil {
		return err
	}
	switch cs.Error {
	case connector.ConnectorStatus_UNSPECIFIED:
		cl := cs.Cluster
		if cl.Connected {
			fmt.Fprintln(out, "Connected")
		} else {
			fmt.Fprintln(out, "Attempting to reconnect...")
		}
		fmt.Fprintf(out, "  Context:       %s (%s)\n", cl.Context, cl.Server)
		if cs.Bridge {
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
			}
		}
	case connector.ConnectorStatus_NOT_STARTED:
		fmt.Fprintln(out, connectorIsNotRunning)
	case connector.ConnectorStatus_DISCONNECTED:
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

func connectorStatus(cmd *cobra.Command) (status *connector.ConnectorStatus, err error) {
	if assertConnectorStarted() != nil {
		return &connector.ConnectorStatus{Error: connector.ConnectorStatus_NOT_STARTED}, nil
	}
	err = withConnector(cmd, func(cs *connectorState) error {
		status, err = cs.grpc.Status(cmd.Context(), &empty.Empty{})
		return err
	})
	return
}

// Pause requests that the network overrides are turned off
func Pause(cmd *cobra.Command, _ []string) error {
	var r *daemon.PauseInfo
	var err error
	err = withDaemon(cmd, func(d daemon.DaemonClient) error {
		r, err = d.Pause(cmd.Context(), &empty.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	var msg string
	switch r.Error {
	case daemon.PauseInfo_UNSPECIFIED:
		stdout := cmd.OutOrStdout()
		fmt.Fprintln(stdout, "Network overrides paused.")
		fmt.Fprintln(stdout, `Use "telepresence resume" to reestablish network overrides.`)
		return nil
	case daemon.PauseInfo_ALREADY_PAUSED:
		msg = "Network overrides are already paused"
	case daemon.PauseInfo_CONNECTED_TO_CLUSTER:
		msg = `Telepresence is connected to a cluster.
See "telepresence status" for details.
Please disconnect before pausing.`
	default:
		msg = fmt.Sprintf("Unexpected error while pausing: %v\n", r.ErrorText)
	}
	return errors.New(msg)
}

// Resume requests that the network overrides are turned back on (after using Pause)
func Resume(cmd *cobra.Command, _ []string) error {
	var r *daemon.ResumeInfo
	var err error
	err = withDaemon(cmd, func(d daemon.DaemonClient) error {
		r, err = d.Resume(cmd.Context(), &empty.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	var msg string
	switch r.Error {
	case daemon.ResumeInfo_UNSPECIFIED:
		fmt.Fprintln(cmd.OutOrStdout(), "Network overrides reestablished.")
		return nil
	case daemon.ResumeInfo_NOT_PAUSED:
		msg = "Network overrides are established (not paused)"
	case daemon.ResumeInfo_REESTABLISHING:
		msg = "Network overrides are being reestablished..."
	default:
		msg = fmt.Sprintf("Unexpected error establishing network overrides: %v", err)
	}
	return errors.New(msg)
}

// Quit sends the quit message to the daemon and waits for it to exit.
func Quit(cmd *cobra.Command, _ []string) error {
	ds, err := newDaemonState(cmd, "", "")
	if err != nil {
		return err
	}
	return ds.DeactivateState()
}

// addIntercept tells the daemon to add a deployment intercept.
func (p *runner) addIntercept(cmd *cobra.Command, _ []string) error {
	err := prepareIntercept(cmd, &p.CreateInterceptRequest)
	if err != nil {
		return err
	}
	ds, cs, err := p.ensureConnected(cmd)
	if err != nil {
		return err
	}
	defer ds.disconnect()
	defer cs.disconnect()

	is := newInterceptState(cs, &p.CreateInterceptRequest, cmd)
	_, err = is.EnsureState()
	return err
}

func prepareIntercept(_ *cobra.Command, ii *manager.CreateInterceptRequest) error {
	var host, portStr string
	spec := ii.InterceptSpec
	hp := strings.SplitN(spec.TargetHost, ":", 2)
	if len(hp) < 2 {
		portStr = hp[0]
	} else {
		host = strings.TrimSpace(hp[0])
		portStr = hp[1]
	}
	if len(host) == 0 {
		host = "127.0.0.1"
	}
	port, err := strconv.Atoi(portStr)

	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("Failed to parse %q as HOST:PORT: %v\n", spec.TargetHost, err))
	}
	spec.TargetHost = host
	spec.TargetPort = int32(port)
	return nil
}

// AvailableIntercepts requests a list of deployments available for intercept from the daemon
func AvailableIntercepts(cmd *cobra.Command, _ []string) error {
	var r *manager.AgentInfoSnapshot
	var err error
	err = withConnector(cmd, func(cs *connectorState) error {
		r, err = cs.grpc.AvailableIntercepts(cmd.Context(), &empty.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	stdout := cmd.OutOrStdout()
	if len(r.Agents) == 0 {
		fmt.Fprintln(stdout, "No interceptable deployments")
		return nil
	}
	fmt.Fprintf(stdout, "Found %d interceptable deployment(s):\n", len(r.Agents))
	for idx, cept := range r.Agents {
		fmt.Fprintf(stdout, "%4d. %s\n", idx+1, cept.Name)
	}
	return nil
}

// ListIntercepts requests a list current intercepts from the daemon
func ListIntercepts(cmd *cobra.Command, _ []string) error {
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

// RemoveIntercept tells the daemon to deactivate and remove an existent intercept
func RemoveIntercept(cmd *cobra.Command, args []string) error {
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

func assertConnectorStarted() error {
	if client.SocketExists(client.ConnectorSocketName) {
		return nil
	}
	return connectorIsNotRunning
}

func assertDaemonStarted() error {
	if client.SocketExists(client.DaemonSocketName) {
		return nil
	}
	return daemonIsNotRunning
}

// withDaemon establishes a connection, calls the function with the gRPC client, and ensures
// that the connection is closed.
func withConnector(cmd *cobra.Command, f func(state *connectorState) error) error {
	ds, err := newDaemonState(cmd, "", "")
	if err != nil {
		return err
	}
	defer ds.disconnect()
	cs, err := newConnectorState(ds.grpc, nil, cmd)
	if err != nil {
		return err
	}
	defer cs.disconnect()
	return f(cs)
}

// withDaemon establishes a connection, calls the function with the gRPC client, and ensures
// that the connection is closed.
func withDaemon(cmd *cobra.Command, f func(daemon.DaemonClient) error) error {
	// OK with dns and fallback empty. Daemon must be up and running
	ds, err := newDaemonState(cmd, "", "")
	if err != nil {
		return err
	}
	defer ds.disconnect()
	return f(ds.grpc)
}

func interceptMessage(ie connector.InterceptError, txt string) string {
	msg := ""
	switch ie {
	case connector.InterceptError_UNSPECIFIED:
	case connector.InterceptError_NO_PREVIEW_HOST:
		msg = `Your cluster is not configured for Preview URLs.
(Could not find a Host resource that enables Path-type Preview URLs.)
Please specify one or more header matches using --match.`
	case connector.InterceptError_NO_CONNECTION:
		msg = connectorIsNotRunning.Error()
	case connector.InterceptError_NO_TRAFFIC_MANAGER:
		msg = "Intercept unavailable: no traffic manager"
	case connector.InterceptError_TRAFFIC_MANAGER_CONNECTING:
		msg = "Connecting to traffic manager..."
	case connector.InterceptError_ALREADY_EXISTS:
		msg = fmt.Sprintf("Intercept with name %q already exists", txt)
	case connector.InterceptError_NO_ACCEPTABLE_DEPLOYMENT:
		msg = fmt.Sprintf("No interceptable deployment matching %s found", txt)
	case connector.InterceptError_TRAFFIC_MANAGER_ERROR:
		msg = txt
	case connector.InterceptError_AMBIGUOUS_MATCH:
		st := &strings.Builder{}
		fmt.Fprintf(st, "Found more than one possible match:")
		for idx, match := range strings.Split(txt, "\n") {
			dn := strings.Split(match, "\t")
			fmt.Fprintf(st, "\n%4d: %s in namespace %s", idx+1, dn[1], dn[0])
		}
		msg = st.String()
	case connector.InterceptError_FAILED_TO_ESTABLISH:
		msg = fmt.Sprintf("Failed to establish intercept: %s", txt)
	case connector.InterceptError_FAILED_TO_REMOVE:
		msg = fmt.Sprintf("Error while removing intercept: %v", txt)
	case connector.InterceptError_NOT_FOUND:
		msg = fmt.Sprintf("Intercept named %q not found", txt)
	}
	return msg
}

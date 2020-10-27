package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/golang/protobuf/ptypes/empty"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/telepresence2/pkg/client"
	"github.com/datawire/telepresence2/pkg/rpc"
)

// IsServerRunning reports whether or not the daemon server is running.
func IsServerRunning() bool {
	return assertDaemonStarted() == nil
}

var daemonIsNotRunning = errors.New("The telepresence daemon has not been started.\nUse 'telepresence [--no-wait]' to start it.")
var connectorIsNotRunning = errors.New("Not connected (use 'telepresence [--no-wait]' to connect to your cluster)")

// version requests version info from the daemon and prints both client and daemon version.
func version(cmd *cobra.Command, _ []string) error {
	av, dv, err := daemonVersion(cmd.OutOrStdout())
	if err == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Client %s\nDaemon v%s (api v%d)\n", client.DisplayVersion(), dv, av)
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
	ds, cs, err := p.ensureConnected(cmd.OutOrStdout())
	if err != nil {
		return err
	}
	cs.disconnect()
	ds.disconnect()
	return nil
}

func (p *runner) ensureConnected(out io.Writer) (*daemonState, *connectorState, error) {
	ds, err := newDaemonState(out, "", "")
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

	cs, err := newConnectorState(ds.grpc, &p.ConnectRequest, out)
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
	cs, err := newConnectorState(nil, nil, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	return cs.DeactivateState()
}

// status will retrieve connectivity status from the daemon and print it on stdout.
func status(cmd *cobra.Command, _ []string) error {
	var ds *rpc.DaemonStatus
	var err error
	if ds, err = daemonStatus(cmd.OutOrStdout()); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	switch ds.Error {
	case rpc.DaemonStatus_NOT_STARTED:
		fmt.Fprintln(out, daemonIsNotRunning)
		return nil
	case rpc.DaemonStatus_PAUSED:
		fmt.Fprintln(out, "Network overrides are paused")
		return nil
	case rpc.DaemonStatus_NO_NETWORK:
		fmt.Fprintln(out, "Network overrides NOT established")
		return nil
	}

	var cs *rpc.ConnectorStatus
	if cs, err = connectorStatus(cmd.OutOrStdout()); err != nil {
		return err
	}
	switch cs.Error {
	case rpc.ConnectorStatus_UNSPECIFIED:
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
		ic := cs.Intercepts
		if ic == nil {
			fmt.Fprintln(out, "  Intercepts:    Unavailable: no traffic manager")
			break
		}
		if ic.Connected {
			fmt.Fprintf(out, "  Interceptable: %d deployments\n", ic.InterceptableCount)
			fmt.Fprintf(out, "  Intercepts:    %d total, %d local\n", ic.ClusterIntercepts, ic.LocalIntercepts)
			if ic.LicenseInfo != "" {
				fmt.Fprintln(out, ic.LicenseInfo)
			}
			break
		}
		if cs.ErrorText != "" {
			fmt.Fprintf(out, "  Intercepts:    %s\n", cs.ErrorText)
		} else {
			fmt.Fprintln(out, "  Intercepts:    (connecting to traffic manager...)")
		}
	case rpc.ConnectorStatus_NOT_STARTED:
		fmt.Fprintln(out, connectorIsNotRunning)
	case rpc.ConnectorStatus_DISCONNECTED:
		fmt.Fprintln(out, "Disconnecting")
	}
	return nil
}

func daemonStatus(out io.Writer) (status *rpc.DaemonStatus, err error) {
	if assertDaemonStarted() != nil {
		return &rpc.DaemonStatus{Error: rpc.DaemonStatus_NOT_STARTED}, nil
	}
	err = withDaemon(out, func(d rpc.DaemonClient) error {
		status, err = d.Status(context.Background(), &empty.Empty{})
		return err
	})
	return
}

func connectorStatus(out io.Writer) (status *rpc.ConnectorStatus, err error) {
	if assertConnectorStarted() != nil {
		return &rpc.ConnectorStatus{Error: rpc.ConnectorStatus_NOT_STARTED}, nil
	}
	err = withConnector(out, func(d rpc.ConnectorClient) error {
		status, err = d.Status(context.Background(), &empty.Empty{})
		return err
	})
	return
}

// Pause requests that the network overrides are turned off
func Pause(cmd *cobra.Command, _ []string) error {
	var r *rpc.PauseInfo
	var err error
	err = withDaemon(cmd.OutOrStdout(), func(d rpc.DaemonClient) error {
		r, err = d.Pause(context.Background(), &empty.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	var msg string
	switch r.Error {
	case rpc.PauseInfo_UNSPECIFIED:
		stdout := cmd.OutOrStdout()
		fmt.Fprintln(stdout, "Network overrides paused.")
		fmt.Fprintln(stdout, `Use "telepresence resume" to reestablish network overrides.`)
		return nil
	case rpc.PauseInfo_ALREADY_PAUSED:
		msg = "Network overrides are already paused"
	case rpc.PauseInfo_CONNECTED_TO_CLUSTER:
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
	var r *rpc.ResumeInfo
	var err error
	err = withDaemon(cmd.OutOrStdout(), func(d rpc.DaemonClient) error {
		r, err = d.Resume(context.Background(), &empty.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	var msg string
	switch r.Error {
	case rpc.ResumeInfo_UNSPECIFIED:
		fmt.Fprintln(cmd.OutOrStdout(), "Network overrides reestablished.")
		return nil
	case rpc.ResumeInfo_NOT_PAUSED:
		msg = "Network overrides are established (not paused)"
	case rpc.ResumeInfo_REESTABLISHING:
		msg = "Network overrides are being reestablished..."
	default:
		msg = fmt.Sprintf("Unexpected error establishing network overrides: %v", err)
	}
	return errors.New(msg)
}

// Quit sends the quit message to the daemon and waits for it to exit.
func Quit(cmd *cobra.Command, _ []string) error {
	ds, err := newDaemonState(cmd.OutOrStdout(), "", "")
	if err != nil {
		return err
	}
	return ds.DeactivateState()
}

// addIntercept tells the daemon to add a deployment intercept.
func (p *runner) addIntercept(cmd *cobra.Command, _ []string) error {
	err := prepareIntercept(cmd, &p.InterceptRequest)
	if err != nil {
		return err
	}
	ds, cs, err := p.ensureConnected(cmd.OutOrStdout())
	if err != nil {
		return err
	}
	defer ds.disconnect()
	defer cs.disconnect()

	is := newInterceptState(cs.grpc, &p.InterceptRequest, cmd.OutOrStdout())
	_, err = is.EnsureState()
	return err
}

func prepareIntercept(cmd *cobra.Command, ii *rpc.InterceptRequest) error {
	if ii.Name == "" {
		ii.Name = ii.Deployment
	}

	// if intercept.Namespace == "" {
	// 	intercept.Namespace = "default"
	// }

	if ii.Prefix == "" {
		ii.Prefix = "/"
	}
	var host, portStr string
	hp := strings.SplitN(ii.TargetHost, ":", 2)
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
		return errors.Wrap(err, fmt.Sprintf("Failed to parse %q as HOST:PORT: %v\n", ii.TargetHost, err))
	}
	ii.TargetHost = host
	ii.TargetPort = int32(port)

	// If the user specifies --preview on the command line, then use its
	// value (--preview is the same as --preview=true, or it could be
	// --preview=false). But if the user does not specify --preview on
	// the command line, compute its value from the presence or absence
	// of --match, since they are mutually exclusive.
	userSetPreviewFlag := cmd.Flags().Changed("preview")
	userSetMatchFlag := len(ii.Patterns) > 0

	switch {
	case userSetPreviewFlag && ii.Preview:
		// User specified --preview (or --preview=true) at the command line
		if userSetMatchFlag {
			return errors.New("Error: Cannot use --match and --preview at the same time")
		}
		// ok: --preview=true and no --match
	case userSetPreviewFlag && !ii.Preview:
		// User specified --preview=false at the command line
		if !userSetMatchFlag {
			return errors.New("Error: Must specify --match when using --preview=false")
		}
		// ok: --preview=false and at least one --match
	default:
		// User did not specify --preview at the command line
		if userSetMatchFlag {
			// ok: at least one --match
			ii.Preview = false
		} else {
			// ok: neither --match nor --preview, fall back to preview
			ii.Preview = true
		}
	}

	if ii.Preview {
		ii.Patterns = make(map[string]string)
		ii.Patterns["x-service-preview"] = client.NewScout("unused").Reporter.InstallID()
	}
	return nil
}

// AvailableIntercepts requests a list of deployments available for intercept from the daemon
func AvailableIntercepts(cmd *cobra.Command, _ []string) error {
	var r *rpc.AvailableInterceptList
	var err error
	err = withConnector(cmd.OutOrStdout(), func(c rpc.ConnectorClient) error {
		r, err = c.AvailableIntercepts(context.Background(), &empty.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_UNSPECIFIED {
		return errors.New(interceptMessage(r.Error, r.Text))
	}
	stdout := cmd.OutOrStdout()
	if len(r.Intercepts) == 0 {
		fmt.Fprintln(stdout, "No interceptable deployments")
		return nil
	}
	fmt.Fprintf(stdout, "Found %d interceptable deployment(s):\n", len(r.Intercepts))
	for idx, cept := range r.Intercepts {
		fmt.Fprintf(stdout, "%4d. %s in namespace %s\n", idx+1, cept.Deployment, cept.Namespace)
	}
	return nil
}

// ListIntercepts requests a list current intercepts from the daemon
func ListIntercepts(cmd *cobra.Command, _ []string) error {
	var r *rpc.InterceptList
	var err error
	err = withConnector(cmd.OutOrStdout(), func(c rpc.ConnectorClient) error {
		r, err = c.ListIntercepts(context.Background(), &empty.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_UNSPECIFIED {
		return errors.New(interceptMessage(r.Error, r.Text))
	}
	stdout := cmd.OutOrStdout()
	if len(r.Intercepts) == 0 {
		fmt.Fprintln(stdout, "No intercepts")
		return nil
	}
	var previewURL string
	for idx, cept := range r.Intercepts {
		fmt.Fprintf(stdout, "%4d. %s\n", idx+1, cept.Name)
		if cept.PreviewUrl != "" {
			previewURL = cept.PreviewUrl
			fmt.Fprintln(stdout, "      (preview URL available)")
		}
		fmt.Fprintf(stdout, "      Intercepting requests to %s when\n", cept.Deployment)
		for k, v := range cept.Patterns {
			fmt.Fprintf(stdout, "      - %s: %s\n", k, v)
		}
		fmt.Fprintf(stdout, "      and redirecting them to %s:%d\n", cept.TargetHost, cept.TargetPort)
	}
	if previewURL != "" {
		fmt.Fprintln(stdout, "Share a preview of your changes with anyone by visiting\n  ", previewURL)
	}
	return nil
}

// RemoveIntercept tells the daemon to deactivate and remove an existent intercept
func RemoveIntercept(cmd *cobra.Command, args []string) error {
	return withConnector(cmd.OutOrStdout(), func(c rpc.ConnectorClient) error {
		is := newInterceptState(c, &rpc.InterceptRequest{Name: strings.TrimSpace(args[0])}, cmd.OutOrStdout())
		return is.DeactivateState()
	})
}

func daemonVersion(out io.Writer) (apiVersion int, version string, err error) {
	err = withDaemon(out, func(d rpc.DaemonClient) error {
		vi, err := d.Version(context.Background(), &empty.Empty{})
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
func withConnector(out io.Writer, f func(rpc.ConnectorClient) error) error {
	ds, err := newDaemonState(out, "", "")
	if err != nil {
		return err
	}
	defer ds.disconnect()
	cs, err := newConnectorState(ds.grpc, nil, out)
	if err != nil {
		return err
	}
	defer cs.disconnect()
	return f(cs.grpc)
}

// withDaemon establishes a connection, calls the function with the gRPC client, and ensures
// that the connection is closed.
func withDaemon(out io.Writer, f func(rpc.DaemonClient) error) error {
	// OK with dns and fallback empty. Daemon must be up and running
	ds, err := newDaemonState(out, "", "")
	if err != nil {
		return err
	}
	defer ds.disconnect()
	return f(ds.grpc)
}

func interceptMessage(ie rpc.InterceptError, txt string) string {
	msg := ""
	switch ie {
	case rpc.InterceptError_UNSPECIFIED:
	case rpc.InterceptError_NO_PREVIEW_HOST:
		msg = `Your cluster is not configured for Preview URLs.
(Could not find a Host resource that enables Path-type Preview URLs.)
Please specify one or more header matches using --match.`
	case rpc.InterceptError_NO_CONNECTION:
		msg = connectorIsNotRunning.Error()
	case rpc.InterceptError_NO_TRAFFIC_MANAGER:
		msg = "Intercept unavailable: no traffic manager"
	case rpc.InterceptError_TRAFFIC_MANAGER_CONNECTING:
		msg = "Connecting to traffic manager..."
	case rpc.InterceptError_ALREADY_EXISTS:
		msg = fmt.Sprintf("Intercept with name %q already exists", txt)
	case rpc.InterceptError_NO_ACCEPTABLE_DEPLOYMENT:
		msg = fmt.Sprintf("No interceptable deployment matching %s found", txt)
	case rpc.InterceptError_TRAFFIC_MANAGER_ERROR:
		msg = txt
	case rpc.InterceptError_AMBIGUOUS_MATCH:
		st := &strings.Builder{}
		fmt.Fprintf(st, "Found more than one possible match:")
		for idx, match := range strings.Split(txt, "\n") {
			dn := strings.Split(match, "\t")
			fmt.Fprintf(st, "\n%4d: %s in namespace %s", idx+1, dn[1], dn[0])
		}
		msg = st.String()
	case rpc.InterceptError_FAILED_TO_ESTABLISH:
		msg = fmt.Sprintf("Failed to establish intercept: %s", txt)
	case rpc.InterceptError_FAILED_TO_REMOVE:
		msg = fmt.Sprintf("Error while removing intercept: %v", txt)
	case rpc.InterceptError_NOT_FOUND:
		msg = fmt.Sprintf("Intercept named %q not found", txt)
	}
	return msg
}

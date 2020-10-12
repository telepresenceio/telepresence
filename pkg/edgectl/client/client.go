package client

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/api/edgectl/rpc"
)

// IsServerRunning reports whether or not the daemon server is running.
func IsServerRunning() bool {
	return assertDaemonStarted() == nil
}

var daemonIsNotRunning = errors.New("The edgectl daemon has not been started.\nUse 'sudo edgectl daemon' to start it.")
var connectorIsNotRunning = errors.New("Not connected (use 'edgectl connect' to connect to your cluster)")

// Version requests version info from the daemon and prints both client and daemon version.
func Version(cmd *cobra.Command, _ []string) error {
	av, dv, err := daemonVersion(cmd.OutOrStdout())
	if err == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Client %s\nDaemon v%s (api v%d)\n", edgectl.DisplayVersion(), dv, av)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Client %s\n", edgectl.DisplayVersion())
	if err != daemonIsNotRunning {
		// Socket exists but connection failed anyway.
		err = fmt.Errorf("Unable to connect to daemon: %s", err)
	}
	return err
}

// A ConnectInfo contains all information needed to connect to a cluster.
type ConnectInfo struct {
	rpc.ConnectRequest
}

// Connect asks the daemon to connect to a cluster
func (ci *ConnectInfo) Connect(cmd *cobra.Command, args []string) error {
	ds, err := newDaemonState(cmd.OutOrStdout(), "", "")
	if err != nil {
		return err
	}
	defer ds.disconnect()

	// When set, wait that number of seconds for network before returning ConnectResponse_EstablishingOverrides
	ci.WaitForNetwork = 0

	cs, err := newConnectorState(ds.grpc, &ci.ConnectRequest, cmd.OutOrStdout())
	defer cs.disconnect()
	if err == nil {
		return errors.New("Already connected")
	}

	_, err = cs.EnsureState()
	return err
}

// Disconnect asks the daemon to disconnect from the connected cluster
func Disconnect(cmd *cobra.Command, _ []string) error {
	cs, err := newConnectorState(nil, nil, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	return cs.DeactivateState()
}

// Status will retrieve connectivity status from the daemon and print it on stdout.
func Status(cmd *cobra.Command, _ []string) error {
	var ds *rpc.DaemonStatusResponse
	var err error
	if ds, err = daemonStatus(cmd.OutOrStdout()); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	switch ds.Error {
	case rpc.DaemonStatusResponse_NotStarted:
		fmt.Fprintln(out, daemonIsNotRunning)
		return nil
	case rpc.DaemonStatusResponse_Paused:
		fmt.Fprintln(out, "Network overrides are paused")
		return nil
	case rpc.DaemonStatusResponse_NoNetwork:
		fmt.Fprintln(out, "Network overrides NOT established")
		return nil
	}

	var cs *rpc.ConnectorStatusResponse
	if cs, err = connectorStatus(cmd.OutOrStdout()); err != nil {
		return err
	}
	switch cs.Error {
	case rpc.ConnectorStatusResponse_Ok:
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
	case rpc.ConnectorStatusResponse_NotStarted:
		fmt.Fprintln(out, connectorIsNotRunning)
	case rpc.ConnectorStatusResponse_Disconnected:
		fmt.Fprintln(out, "Disconnecting")
	}
	return nil
}

func daemonStatus(out io.Writer) (status *rpc.DaemonStatusResponse, err error) {
	if assertDaemonStarted() != nil {
		return &rpc.DaemonStatusResponse{Error: rpc.DaemonStatusResponse_NotStarted}, nil
	}
	err = withDaemon(out, func(d rpc.DaemonClient) error {
		status, err = d.Status(context.Background(), &rpc.Empty{})
		return err
	})
	return
}

func connectorStatus(out io.Writer) (status *rpc.ConnectorStatusResponse, err error) {
	if assertConnectorStarted() != nil {
		return &rpc.ConnectorStatusResponse{Error: rpc.ConnectorStatusResponse_NotStarted}, nil
	}
	err = withConnector(out, func(d rpc.ConnectorClient) error {
		status, err = d.Status(context.Background(), &rpc.Empty{})
		return err
	})
	return
}

// Pause requests that the network overrides are turned off
func Pause(cmd *cobra.Command, _ []string) error {
	var r *rpc.PauseResponse
	var err error
	err = withDaemon(cmd.OutOrStdout(), func(d rpc.DaemonClient) error {
		r, err = d.Pause(context.Background(), &rpc.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	var msg string
	switch r.Error {
	case rpc.PauseResponse_Ok:
		stdout := cmd.OutOrStdout()
		fmt.Fprintln(stdout, "Network overrides paused.")
		fmt.Fprintln(stdout, `Use "edgectl resume" to reestablish network overrides.`)
		return nil
	case rpc.PauseResponse_AlreadyPaused:
		msg = "Network overrides are already paused"
	case rpc.PauseResponse_ConnectedToCluster:
		msg = `Edge Control is connected to a cluster.
See "edgectl status" for details.
Please disconnect before pausing.`
	default:
		msg = fmt.Sprintf("Unexpected error while pausing: %v\n", r.ErrorText)
	}
	return errors.New(msg)
}

// Resume requests that the network overrides are turned back on (after using Pause)
func Resume(cmd *cobra.Command, _ []string) error {
	var r *rpc.ResumeResponse
	var err error
	err = withDaemon(cmd.OutOrStdout(), func(d rpc.DaemonClient) error {
		r, err = d.Resume(context.Background(), &rpc.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	var msg string
	switch r.Error {
	case rpc.ResumeResponse_Ok:
		fmt.Fprintln(cmd.OutOrStdout(), "Network overrides reestablished.")
		return nil
	case rpc.ResumeResponse_NotPaused:
		msg = "Network overrides are established (not paused)"
	case rpc.ResumeResponse_ReEstablishing:
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

// An InterceptInfo contains all information needed to add a deployment intercept.
type InterceptInfo struct {
	rpc.InterceptRequest
}

// AddIntercept tells the daemon to add a deployment intercept.
func (ii *InterceptInfo) AddIntercept(cmd *cobra.Command, args []string) error {
	ii.Deployment = args[0]
	err := prepareIntercept(cmd, &ii.InterceptRequest)
	if err != nil {
		return err
	}
	return withConnector(cmd.OutOrStdout(), func(c rpc.ConnectorClient) error {
		is := newInterceptState(c, &ii.InterceptRequest, cmd.OutOrStdout())
		_, err = is.EnsureState()
		return err
	})
}

func prepareIntercept(cmd *cobra.Command, ii *rpc.InterceptRequest) error {
	if ii.Name == "" {
		ii.Name = fmt.Sprintf("cept-%d", time.Now().Unix())
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
		return errors.Wrap(err, fmt.Sprintf("Failed to parse %q as HOST:PORT: %v\n", ii.TargetHost))
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
		ii.Patterns["x-service-preview"] = NewScout("unused").Reporter.InstallID()
	}
	return nil
}

// AvailableIntercepts requests a list of deployments available for intercept from the daemon
func AvailableIntercepts(cmd *cobra.Command, _ []string) error {
	var r *rpc.AvailableInterceptsResponse
	var err error
	err = withConnector(cmd.OutOrStdout(), func(c rpc.ConnectorClient) error {
		r, err = c.AvailableIntercepts(context.Background(), &rpc.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_InterceptOk {
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
	var r *rpc.ListInterceptsResponse
	var err error
	err = withConnector(cmd.OutOrStdout(), func(c rpc.ConnectorClient) error {
		r, err = c.ListIntercepts(context.Background(), &rpc.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_InterceptOk {
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
		if cept.PreviewURL != "" {
			previewURL = cept.PreviewURL
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
		vi, err := d.Version(context.Background(), &rpc.Empty{})
		if err == nil {
			apiVersion = int(vi.APIVersion)
			version = vi.Version
		}
		return err
	})
	return
}

func assertConnectorStarted() error {
	if edgectl.SocketExists(edgectl.ConnectorSocketName) {
		return nil
	}
	return connectorIsNotRunning
}

func assertDaemonStarted() error {
	if edgectl.SocketExists(edgectl.DaemonSocketName) {
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
	case rpc.InterceptError_InterceptOk:
	case rpc.InterceptError_NoPreviewHost:
		msg = `Your cluster is not configured for Preview URLs.
(Could not find a Host resource that enables Path-type Preview URLs.)
Please specify one or more header matches using --match.`
	case rpc.InterceptError_NoConnection:
		msg = connectorIsNotRunning.Error()
	case rpc.InterceptError_NoTrafficManager:
		msg = "Intercept unavailable: no traffic manager"
	case rpc.InterceptError_TrafficManagerConnecting:
		msg = "Connecting to traffic manager..."
	case rpc.InterceptError_AlreadyExists:
		msg = fmt.Sprintf("Intercept with name %q already exists", txt)
	case rpc.InterceptError_NoAcceptableDeployment:
		msg = fmt.Sprintf("No interceptable deployment matching %s found", txt)
	case rpc.InterceptError_TrafficManagerError:
		msg = txt
	case rpc.InterceptError_AmbiguousMatch:
		st := &strings.Builder{}
		fmt.Fprintf(st, "Found more than one possible match:")
		for idx, match := range strings.Split(txt, "\n") {
			dn := strings.Split(match, "\t")
			fmt.Fprintf(st, "\n%4d: %s in namespace %s", idx+1, dn[1], dn[0])
		}
		msg = st.String()
	case rpc.InterceptError_FailedToEstablish:
		msg = fmt.Sprintf("Failed to establish intercept: %s", txt)
	case rpc.InterceptError_FailedToRemove:
		msg = fmt.Sprintf("Error while removing intercept: %v", txt)
	case rpc.InterceptError_NotFound:
		msg = fmt.Sprintf("Intercept named %q not found", txt)
	}
	return msg
}

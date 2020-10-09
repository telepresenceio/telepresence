package client

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/api/edgectl/rpc"
)

// IsServerRunning reports whether or not the daemon server is running.
func IsServerRunning() bool {
	return assertDaemonStarted() == nil
}

var daemonIsNotRunning = errors.New("Daemon is not running (see \"edgectl help daemon\")")

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
func (x *ConnectInfo) Connect(cmd *cobra.Command, args []string) error {
	ds, err := daemonStatus(cmd.OutOrStdout())
	if err != nil {
		return err
	}
	switch ds.Error {
	case rpc.DaemonStatusResponse_Ok:
	case rpc.DaemonStatusResponse_NotStarted:
		return assertDaemonStarted()
	case rpc.DaemonStatusResponse_NoNetwork:
		return errors.New("Unable to connect: Network overrides are not established")
	case rpc.DaemonStatusResponse_Paused:
		return errors.New("Unable to connect: Network overrides are paused (use 'edgectl resume')")
	}

	if assertConnectorStarted() == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "Already connected")
		return nil
	}

	connectorCmd := exec.Command(edgectl.GetExe(), "connector-foreground")
	connectorCmd.Env = os.Environ()
	if err = connectorCmd.Start(); err != nil {
		return errors.Wrap(err, "failed to launch the connector service")
	}

	x.Args = args
	x.InstallID = NewScout("unused").Reporter.InstallID()

	// When set, wait that number of seconds for network before returning ConnectResponse_EstablishingOverrides
	x.WaitForNetwork = 0

	// TODO: Progress reporting during connect. Either divide into several calls and report completion
	//  of each, or use a stream. Can be made as part of ticket #1334.
	var r *rpc.ConnectResponse
	fmt.Fprintf(cmd.OutOrStdout(), "Connecting to traffic manager in namespace %s...\n", x.ManagerNS)

	success := false
	for count := 0; count < 40; count++ {
		err = withConnector(func(c rpc.ConnectorClient) error {
			r, err = c.Connect(context.Background(), &x.ConnectRequest)
			success = err == nil
			return err
		})
		if success {
			break
		}
		if count == 4 {
			fmt.Println("Waiting for connector to start...")
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !success {
		sb := strings.Builder{}
		sb.WriteString("Connector service did not come up!")
		if err != nil {
			sb.WriteString("\nError was: ")
			sb.WriteString(err.Error())
		}
		sb.WriteString("\nTake a look at ")
		sb.WriteString(edgectl.Logfile)
		sb.WriteString(" for more information.")
		err = errors.New(sb.String())
	}
	if err != nil {
		return err
	}

	var msg string
	switch r.Error {
	case rpc.ConnectResponse_Ok:
		fmt.Fprintf(cmd.OutOrStdout(), "Connected to context %s (%s)\n", r.ClusterContext, r.ClusterServer)
		return nil
	case rpc.ConnectResponse_AlreadyConnected:
		fmt.Fprintln(cmd.OutOrStdout(), "Already connected")
		return nil
	case rpc.ConnectResponse_TrafficManagerFailed:
		fmt.Fprintf(cmd.OutOrStdout(), `Connected to context %s (%s)

Unable to connect to the traffic manager.
The intercept feature will not be available.
Error was: %s
`, r.ClusterContext, r.ClusterServer, r.ErrorText)

		// The connect is considered a success. There's still a cluster connection and bridge.
		return nil
	case rpc.ConnectResponse_Disconnecting:
		msg = "Unable to connect while disconnecting"
	case rpc.ConnectResponse_ClusterFailed, rpc.ConnectResponse_BridgeFailed:
		msg = r.ErrorText
	}
	return errors.New(msg)
}

// Disconnect asks the daemon to disconnect from the connected cluster
func Disconnect(_ *cobra.Command, _ []string) error {
	if assertConnectorStarted() != nil {
		return errors.New("Not connected")
	}
	var err error
	err = withConnector(func(d rpc.ConnectorClient) error {
		_, err = d.Quit(context.Background(), &rpc.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	return edgectl.WaitUntilSocketVanishes("connector", edgectl.ConnectorSocketName, 5*time.Second)
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
		fmt.Fprintln(out, "Daemon is not started. Use 'sudo edgectl daemon' to start it.")
		return nil
	case rpc.DaemonStatusResponse_Paused:
		fmt.Fprintln(out, "Network overrides are paused")
		return nil
	case rpc.DaemonStatusResponse_NoNetwork:
		fmt.Fprintln(out, "Network overrides NOT established")
		return nil
	}

	var cs *rpc.ConnectorStatusResponse
	if cs, err = connectorStatus(); err != nil {
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
		fmt.Fprintln(out, "Not connected (use 'edgectl connect' to connect to your cluster)")
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

func connectorStatus() (status *rpc.ConnectorStatusResponse, err error) {
	if assertConnectorStarted() != nil {
		return &rpc.ConnectorStatusResponse{Error: rpc.ConnectorStatusResponse_NotStarted}, nil
	}
	err = withConnector(func(d rpc.ConnectorClient) error {
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
func (x *InterceptInfo) AddIntercept(cmd *cobra.Command, args []string) error {
	x.Deployment = args[0]
	if x.Name == "" {
		x.Name = fmt.Sprintf("cept-%d", time.Now().Unix())
	}

	// if intercept.Namespace == "" {
	// 	intercept.Namespace = "default"
	// }

	if x.Prefix == "" {
		x.Prefix = "/"
	}
	var host, portStr string
	hp := strings.SplitN(x.TargetHost, ":", 2)
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
		return errors.Wrap(err, fmt.Sprintf("Failed to parse %q as HOST:PORT: %v\n", x.TargetHost))
	}
	x.TargetHost = host
	x.TargetPort = int32(port)

	// If the user specifies --preview on the command line, then use its
	// value (--preview is the same as --preview=true, or it could be
	// --preview=false). But if the user does not specify --preview on
	// the command line, compute its value from the presence or absence
	// of --match, since they are mutually exclusive.
	userSetPreviewFlag := cmd.Flags().Changed("preview")
	userSetMatchFlag := len(x.Patterns) > 0

	switch {
	case userSetPreviewFlag && x.Preview:
		// User specified --preview (or --preview=true) at the command line
		if userSetMatchFlag {
			return errors.New("Error: Cannot use --match and --preview at the same time")
		}
		// ok: --preview=true and no --match
	case userSetPreviewFlag && !x.Preview:
		// User specified --preview=false at the command line
		if !userSetMatchFlag {
			return errors.New("Error: Must specify --match when using --preview=false")
		}
		// ok: --preview=false and at least one --match
	default:
		// User did not specify --preview at the command line
		if userSetMatchFlag {
			// ok: at least one --match
			x.Preview = false
		} else {
			// ok: neither --match nor --preview, fall back to preview
			x.Preview = true
		}
	}

	if x.Preview {
		x.Patterns = make(map[string]string)
		x.Patterns["x-service-preview"] = NewScout("unused").Reporter.InstallID()
	}

	var r *rpc.InterceptResponse
	err = withConnector(func(c rpc.ConnectorClient) error {
		r, err = c.AddIntercept(context.Background(), &x.InterceptRequest)
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_InterceptOk {
		return errors.New(interceptMessage(r.Error, r.Text))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Using deployment %s in namespace %s\n", x.Deployment, r.Text)

	if r.PreviewURL != "" {
		fmt.Fprintln(cmd.OutOrStdout(), "Share a preview of your changes with anyone by visiting\n  ", r.PreviewURL)
	}
	return nil
}

// AvailableIntercepts requests a list of deployments available for intercept from the daemon
func AvailableIntercepts(cmd *cobra.Command, _ []string) error {
	var r *rpc.AvailableInterceptsResponse
	var err error
	err = withConnector(func(c rpc.ConnectorClient) error {
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
	err = withConnector(func(c rpc.ConnectorClient) error {
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
	name := strings.TrimSpace(args[0])
	var r *rpc.InterceptResponse
	var err error
	err = withConnector(func(c rpc.ConnectorClient) error {
		r, err = c.RemoveIntercept(context.Background(), &rpc.RemoveInterceptRequest{Name: name})
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_InterceptOk {
		return errors.New(interceptMessage(r.Error, r.Text))
	}
	return nil
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
	return errors.New("Not connected (use 'edgectl connect' to connect to your cluster)")
}

func assertDaemonStarted() error {
	if edgectl.SocketExists(edgectl.DaemonSocketName) {
		return nil
	}
	return errors.New("The edgectl daemon has not been started.\nUse 'sudo edgectl daemon' to start it.")
}

// withDaemon establishes a connection, calls the function with the gRPC client, and ensures
// that the connection is closed.
func withConnector(f func(rpc.ConnectorClient) error) error {
	var err error
	if err = assertDaemonStarted(); err != nil {
		return err
	}
	if err = assertConnectorStarted(); err != nil {
		return err
	}
	var conn *grpc.ClientConn
	if conn, err = grpc.Dial(edgectl.SocketURL(edgectl.ConnectorSocketName), grpc.WithInsecure()); err != nil {
		return err
	}
	defer conn.Close()
	return f(rpc.NewConnectorClient(conn))
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
		msg = "Not connected (use 'edgectl connect' to connect to your cluster)"
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

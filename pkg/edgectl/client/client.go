package client

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/api/edgectl/rpc"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
)

// IsServerRunning reports whether or not the daemon server is running.
func IsServerRunning() bool {
	_, _, err := daemonVersion()
	return err == nil
}

// Version requests version info from the daemon and prints both client and daemon version.
func Version(cmd *cobra.Command, _ []string) error {
	av, dv, err := daemonVersion()
	if err == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Client %s\nDaemon v%s (api v%d)\n", edgectl.DisplayVersion(), dv, av)
		os.Exit(0)
	}
	return err
}

// A ConnectInfo contains all information needed to connect to a cluster.
type ConnectInfo struct {
	rpc.ConnectRequest
}

// Connect asks the daemon to connect to a cluster
func (x *ConnectInfo) Connect(cmd *cobra.Command, args []string) error {
	u, err := getRunAsInfo()
	if err != nil {
		return err
	}
	x.User = u
	x.Args = args
	x.InstallID = NewScout("unused").Reporter.InstallID()

	// When set, wait that number of seconds for network before returning ConnectResponse_EstablishingOverrides
	x.WaitForNetwork = 0

	// TODO: Progress reporting during connect. Either divide into several calls and report completion
	//  of each, or use a stream. Can be made as part of ticket #1334.
	var r *rpc.ConnectResponse
	fmt.Fprintf(cmd.OutOrStdout(), "Connecting to traffic manager in namespace %s...\n", x.ManagerNS)
	err = withDaemon(func(c rpc.DaemonClient) error {
		r, err = c.Connect(context.Background(), &x.ConnectRequest)
		return err
	})
	if err != nil {
		return err
	}

	stderr := cmd.OutOrStderr()
	switch r.Error {
	case rpc.ConnectResponse_Ok:
		fmt.Fprintf(cmd.OutOrStdout(), "Connected to context %s (%s)\n", r.ClusterContext, r.ClusterServer)
		return nil
	case rpc.ConnectResponse_AlreadyConnected:
		fmt.Fprintln(cmd.OutOrStdout(), "Already connected")
		return nil
	case rpc.ConnectResponse_Disconnecting:
		fmt.Fprintln(stderr, "Not ready: Trying to disconnect")
	case rpc.ConnectResponse_Paused:
		fmt.Fprintln(stderr, "Not ready: Network overrides are paused (use \"edgectl resume\")")
	case rpc.ConnectResponse_EstablishingOverrides:
		fmt.Fprintln(stderr, "Not ready: Establishing network overrides")
	case rpc.ConnectResponse_ClusterFailed, rpc.ConnectResponse_BridgeFailed:
		fmt.Fprintln(stderr, r.ErrorText)
	case rpc.ConnectResponse_TrafficManagerFailed:
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Unable to connect to the traffic manager in your cluster.")
		fmt.Fprintln(stderr, "The intercept feature will not be available.")
		fmt.Fprintln(stderr, "Error was:", r.ErrorText)
	}
	os.Exit(1)
	return nil
}

// Disconnect asks the daemon to disconnect from the connected cluster
func Disconnect(cmd *cobra.Command, _ []string) error {
	return withDaemon(func(d rpc.DaemonClient) error {
		r, err := d.Disconnect(context.Background(), &rpc.Empty{})
		if err != nil {
			return err
		}
		switch r.Error {
		case rpc.DisconnectResponse_Ok:
			return nil
		case rpc.DisconnectResponse_NotConnected:
			fmt.Fprintln(cmd.OutOrStderr(), "Not connected (use 'edgectl connect' to connect to your cluster)")
		case rpc.DisconnectResponse_DisconnectFailed:
			fmt.Fprintln(cmd.OutOrStderr(), r.ErrorText)
		}
		os.Exit(1)
		return nil
	})
}

// Status will retrieve connectivity status from the daemon and print it on stdout.
func Status(cmd *cobra.Command, _ []string) error {
	return withDaemon(func(d rpc.DaemonClient) error {
		out := cmd.OutOrStdout()
		s, err := d.Status(context.Background(), &rpc.Empty{})
		if err != nil {
			return err
		}
		switch s.Error {
		case rpc.StatusResponse_Ok:
			cl := s.Cluster
			if cl.Connected {
				fmt.Fprintln(out, "Connected")
			} else {
				fmt.Fprintln(out, "Attempting to reconnect...")
			}
			fmt.Fprintf(out, "  Context:       %s (%s)\n", cl.Context, cl.Server)
			if s.Bridge {
				fmt.Fprintln(out, "  Proxy:         ON (networking to the cluster is enabled)")
			} else {
				fmt.Fprintln(out, "  Proxy:         OFF (attempting to connect...)")
			}
			ic := s.Intercepts
			if ic == nil {
				fmt.Fprintln(out, "  Intercepts:    Unavailable: no traffic manager")
				break
			}
			if ic.Connected {
				fmt.Fprintf(out, "  Interceptable: %d deployments\n", ic.InterceptableCount)
				fmt.Fprintf(out, "  Intercepts:    %d total, %d local\n", ic.ClusterIntercepts, ic.LocalIntercepts)
				break
			}
			if s.ErrorText != "" {
				fmt.Fprintf(out, "  Intercepts:    %s\n", s.ErrorText)
			} else {
				fmt.Fprintln(out, "  Intercepts:    (connecting to traffic manager...)")
			}
		case rpc.StatusResponse_Paused:
			fmt.Fprintln(out, "Network overrides are paused")
		case rpc.StatusResponse_NoNetwork:
			fmt.Fprintln(out, "Network overrides NOT established")
		case rpc.StatusResponse_Disconnected:
			fmt.Fprintln(out, "Not connected (use 'edgectl connect' to connect to your cluster)")
		}
		return nil
	})
}

// Pause requests that the network overrides are turned off
func Pause(cmd *cobra.Command, _ []string) error {
	var r *rpc.PauseResponse
	var err error
	err = withDaemon(func(d rpc.DaemonClient) error {
		r, err = d.Pause(context.Background(), &rpc.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	stderr := cmd.OutOrStderr()
	switch r.Error {
	case rpc.PauseResponse_Ok:
		stdout := cmd.OutOrStdout()
		fmt.Fprintln(stdout, "Network overrides paused.")
		fmt.Fprintln(stdout, `Use "edgectl resume" to reestablish network overrides.`)
		return nil
	case rpc.PauseResponse_AlreadyPaused:
		fmt.Fprintln(stderr, "Network overrides are already paused")
	case rpc.PauseResponse_ConnectedToCluster:
		fmt.Fprintln(stderr, "Edge Control is connected to a cluster.")
		fmt.Fprintln(stderr, "See \"edgectl status\" for details.")
		fmt.Fprintln(stderr, "Please disconnect before pausing.")
	default:
		fmt.Fprintf(stderr, "Unexpected error while pausing: %v\n", r.ErrorText)
	}
	os.Exit(1)
	return nil
}

// Resume requests that the network overrides are turned back on (after using Pause)
func Resume(cmd *cobra.Command, _ []string) error {
	var r *rpc.ResumeResponse
	var err error
	err = withDaemon(func(d rpc.DaemonClient) error {
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
	fmt.Fprintln(cmd.OutOrStderr(), msg)
	os.Exit(1)
	return nil
}

// Quit sends the quit message to the daemon and waits for it to exit.
func Quit(cmd *cobra.Command, _ []string) error {
	return withDaemon(func(d rpc.DaemonClient) error {
		fmt.Fprintln(cmd.OutOrStdout(), "Edge Control Daemon quitting...")
		_, err := d.Quit(context.Background(), &rpc.Empty{})
		return err
	})
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

	stderr := cmd.OutOrStderr()
	if err != nil {
		fmt.Fprintf(stderr, "Failed to parse %q as HOST:PORT: %v\n", x.TargetHost, err)
		os.Exit(1)
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
			fmt.Fprintln(stderr, "Error: Cannot use --match and --preview at the same time")
			os.Exit(1)
		}
		// ok: --preview=true and no --match
	case userSetPreviewFlag && !x.Preview:
		// User specified --preview=false at the command line
		if !userSetMatchFlag {
			fmt.Fprintln(stderr, "Error: Must specify --match when using --preview=false")
			os.Exit(1)
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
	err = withDaemon(func(c rpc.DaemonClient) error {
		var err error
		r, err = c.AddIntercept(context.Background(), &x.InterceptRequest)
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_InterceptOk {
		fmt.Fprintln(cmd.OutOrStderr(), interceptMessage(r.Error, r.Text))
		os.Exit(1)
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
	err := withDaemon(func(c rpc.DaemonClient) error {
		var err error
		r, err = c.AvailableIntercepts(context.Background(), &rpc.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_InterceptOk {
		fmt.Fprintln(cmd.OutOrStderr(), interceptMessage(r.Error, r.Text))
		os.Exit(1)
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
	err := withDaemon(func(c rpc.DaemonClient) error {
		var err error
		r, err = c.ListIntercepts(context.Background(), &rpc.Empty{})
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_InterceptOk {
		fmt.Fprintln(cmd.OutOrStderr(), interceptMessage(r.Error, r.Text))
		os.Exit(1)
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
	err := withDaemon(func(c rpc.DaemonClient) error {
		var err error
		r, err = c.RemoveIntercept(context.Background(), &rpc.RemoveInterceptRequest{Name: name})
		return err
	})
	if err != nil {
		return err
	}
	if r.Error != rpc.InterceptError_InterceptOk {
		fmt.Fprintln(cmd.OutOrStderr(), interceptMessage(r.Error, r.Text))
		os.Exit(1)
	}
	return nil
}

func daemonVersion() (apiVersion int, version string, err error) {
	err = withDaemon(func(d rpc.DaemonClient) error {
		vi, err := d.Version(context.Background(), &rpc.Empty{})
		if err == nil {
			apiVersion = int(vi.APIVersion)
			version = vi.Version
		}
		return err
	})
	return
}

// withDaemon establishes a connection, calls the function with the gRPC client, and ensures
// that the connection is closed.
func withDaemon(f func(rpc.DaemonClient) error) error {
	// TODO: Revise use of passthrough once this is fixed in grpc-go.
	//  see: https://github.com/grpc/grpc-go/issues/1741
	//  and https://github.com/grpc/grpc-go/issues/1911
	conn, err := grpc.Dial("passthrough:///unix://"+edgectl.DaemonSocketName, grpc.WithInsecure())
	if err == nil {
		defer conn.Close()
		return f(rpc.NewDaemonClient(conn))
	}
	return err
}

func getRunAsInfo() (*rpc.ConnectRequest_UserInfo, error) {
	usr, err := user.Current()
	if err != nil {
		return nil, errors.Wrap(err, "user.Current()")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, errors.Wrap(err, "os.Getwd()")
	}
	rai := &rpc.ConnectRequest_UserInfo{
		Name: usr.Username,
		Cwd:  cwd,
		Env:  os.Environ(),
	}
	return rai, nil
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

package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

func statusCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "status",
		Args: cobra.NoArgs,

		Short: "Show connectivity status",
		RunE:  status,
	}
}

// status will retrieve connectivity status from the daemon and print it on stdout.
func status(cmd *cobra.Command, _ []string) error {
	if err := daemonStatus(cmd); err != nil {
		return err
	}

	if err := connectorStatus(cmd); err != nil {
		return err
	}

	return nil
}

func daemonStatus(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	var status *daemon.DaemonStatus
	var version *common.VersionInfo
	err := withStartedDaemon(cmd, func(ds *daemonState) error {
		var err error
		status, err = ds.grpc.Status(cmd.Context(), &empty.Empty{})
		if err != nil {
			return err
		}
		version, err = ds.grpc.Version(cmd.Context(), &empty.Empty{})
		if err != nil {
			return err
		}
		return nil
	})
	if err == errDaemonIsNotRunning {
		err = nil
		status = &daemon.DaemonStatus{Error: daemon.DaemonStatus_NOT_STARTED}
	}
	if err != nil {
		return err
	}

	switch status.Error {
	case daemon.DaemonStatus_NOT_STARTED:
		fmt.Fprintln(out, "Root Daemon: Not running")
		return nil
	case daemon.DaemonStatus_NO_NETWORK:
		fmt.Fprintln(out, "Root Daemon: Running, network overrides NOT established")
	case daemon.DaemonStatus_UNSPECIFIED:
		fmt.Fprintln(out, "Root Daemon: Running")
	}
	fmt.Fprintf(out, "  Version     : %s (api %d)\n", version.Version, version.ApiVersion)
	fmt.Fprintf(out, "  DNS : %q\n", status.Dns)
	return nil
}

func connectorStatus(cmd *cobra.Command) error {
	out := cmd.OutOrStdout()

	if !cliutil.IsConnectorRunning() {
		fmt.Fprintln(out, "User Daemon: Not running")
		return nil
	}
	fmt.Fprintln(out, "User Daemon: Running")

	type kv struct {
		Key   string
		Value string
	}
	var fields []kv
	defer func() {
		klen := 0
		for _, kv := range fields {
			if len(kv.Key) > klen {
				klen = len(kv.Key)
			}
		}
		for _, kv := range fields {
			vlines := strings.Split(strings.TrimSpace(kv.Value), "\n")
			fmt.Fprintf(out, "  %-*s: %s\n", klen, kv.Key, vlines[0])
			for _, vline := range vlines[1:] {
				fmt.Fprintf(out, "    %s\n", vline)
			}
		}
	}()

	return cliutil.WithConnector(cmd.Context(), func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		version, err := connectorClient.Version(ctx, &empty.Empty{})
		if err != nil {
			return err
		}
		fields = append(fields, kv{"Version", fmt.Sprintf("%s (api %d)", version.Version, version.ApiVersion)})

		if !cliutil.HasLoggedIn(ctx) {
			fields = append(fields, kv{"Ambassador Cloud", "Logged out"})
		} else if _, err := cliutil.GetCloudAccessToken(ctx, false); err != nil {
			fields = append(fields, kv{"Ambassador Cloud", "Login expired"})
		} else {
			fields = append(fields, kv{"Ambassador Cloud", "Logged in"})
		}

		status, err := connectorClient.Status(ctx, &connector.ConnectRequest{
			KubeFlags: kubeFlagMap(),
		})
		if err != nil {
			return err
		}
		switch status.Error {
		case connector.ConnectInfo_UNSPECIFIED, connector.ConnectInfo_ALREADY_CONNECTED:
			fields = append(fields, kv{"Status", "Connected"})
		case connector.ConnectInfo_MUST_RESTART:
			fields = append(fields, kv{"Status", "Connected, but must restart"})
		case connector.ConnectInfo_DISCONNECTED:
			fields = append(fields, kv{"Status", "Not connected"})
			return nil
		case connector.ConnectInfo_CLUSTER_FAILED:
			fields = append(fields, kv{"Status", "Not connected, error talking to cluster"})
			fields = append(fields, kv{"Error", status.ErrorText})
			return nil
		case connector.ConnectInfo_TRAFFIC_MANAGER_FAILED:
			fields = append(fields, kv{"Status", "Not connected, error talking to in-cluster Telepresence traffic-manager"})
			fields = append(fields, kv{"Error", status.ErrorText})
			return nil
		}
		fields = append(fields, kv{"Kubernetes server", status.ClusterServer})
		fields = append(fields, kv{"Kubernetes context", status.ClusterContext})
		if status.BridgeOk {
			fields = append(fields, kv{"Telepresence proxy", "ON (networking to the cluster is enabled)"})
		} else {
			fields = append(fields, kv{"Telepresence proxy", "OFF (attempting to connect...)"})
		}
		intercepts := fmt.Sprintf("%d total\n", len(status.GetIntercepts().GetIntercepts()))
		for _, icept := range status.GetIntercepts().GetIntercepts() {
			intercepts += fmt.Sprintf("%s: %s\n", icept.Spec.Name, icept.Spec.Client)
		}
		fields = append(fields, kv{"Intercepts", intercepts})

		return nil
	})
}

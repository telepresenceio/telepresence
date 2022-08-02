package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "version",
		Args: cobra.NoArgs,

		Short:   "Show version",
		PreRunE: cliutil.ForcedUpdateCheck,
		RunE:    printVersion,
	}
}

// printVersion requests version info from the daemon and prints both client and daemon version.
func printVersion(cmd *cobra.Command, _ []string) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Client: %s\n",
		client.DisplayVersion())

	var retErr error

	version, err := daemonVersion(cmd.Context())
	switch {
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "Root Daemon: %s (api v%d)\n",
			version.Version, version.ApiVersion)
	case err == cliutil.ErrNoNetwork:
		fmt.Fprintf(cmd.OutOrStdout(), "Root Daemon: not running\n")
	default:
		fmt.Fprintf(cmd.OutOrStdout(), "Root Daemon: error: %v\n", err)
		retErr = err
	}

	version, err = connectorVersion(cmd.Context())
	switch {
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "User Daemon: %s (api v%d)\n",
			version.Version, version.ApiVersion)
	case err == cliutil.ErrNoUserDaemon:
		fmt.Fprintf(cmd.OutOrStdout(), "User Daemon: not running\n")
	default:
		fmt.Fprintf(cmd.OutOrStdout(), "User Daemon: error: %v\n", err)
		retErr = err
	}

	return retErr
}

func daemonVersion(ctx context.Context) (*common.VersionInfo, error) {
	var version *common.VersionInfo
	err := cliutil.WithStartedNetwork(ctx, func(ctx context.Context, daemonClient daemon.DaemonClient) error {
		var err error
		version, err = daemonClient.Version(ctx, &empty.Empty{})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return version, nil
}

func connectorVersion(ctx context.Context) (*common.VersionInfo, error) {
	var version *common.VersionInfo
	err := cliutil.WithStartedConnector(ctx, false, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var err error
		version, err = connectorClient.Version(ctx, &empty.Empty{})
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return version, nil
}

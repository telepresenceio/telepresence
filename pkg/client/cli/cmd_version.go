package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
)

func versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:  "version",
		Args: cobra.NoArgs,

		Short:   "Show version",
		PreRunE: util.ForcedUpdateCheck,
		RunE:    PrintVersion,
		Annotations: map[string]string{
			ann.RootDaemon: ann.Optional,
			ann.UserDaemon: ann.Optional,
		},
	}
}

// PrintVersion requests version info from the daemon and prints both client and daemon version.
func PrintVersion(cmd *cobra.Command, _ []string) error {
	if err := util.InitCommand(cmd); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Client: %s\n",
		client.DisplayVersion())

	ctx := cmd.Context()
	version, err := daemonVersion(ctx)
	switch {
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "Root Daemon: %s (api v%d)\n", version.Version, version.ApiVersion)
	case err == util.ErrNoRootDaemon:
		fmt.Fprintf(cmd.OutOrStdout(), "Root Daemon: not running\n")
	default:
		fmt.Fprintf(cmd.OutOrStdout(), "Root Daemon: error: %v\n", err)
	}

	version, err = connectorVersion(ctx)
	switch {
	case err == nil:
		fmt.Fprintf(cmd.OutOrStdout(), "User Daemon: %s (api v%d)\n", version.Version, version.ApiVersion)
	case err == util.ErrNoUserDaemon:
		fmt.Fprintf(cmd.OutOrStdout(), "User Daemon: not running\n")
	default:
		fmt.Fprintf(cmd.OutOrStdout(), "User Daemon: error: %v\n", err)
	}
	return nil
}

func daemonVersion(ctx context.Context) (*common.VersionInfo, error) {
	if conn, err := client.DialSocket(ctx, client.DaemonSocketName); err == nil {
		defer conn.Close()
		return daemon.NewDaemonClient(conn).Version(ctx, &empty.Empty{})
	}
	return nil, util.ErrNoRootDaemon
}

func connectorVersion(ctx context.Context) (*common.VersionInfo, error) {
	if userD := util.GetUserDaemon(ctx); userD != nil {
		return userD.Version(ctx, &empty.Empty{})
	}
	return nil, util.ErrNoUserDaemon
}

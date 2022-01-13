package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	empty "google.golang.org/protobuf/types/known/emptypb"
)

func getRemoteCommands(ctx context.Context) (cliutil.CommandGroups, error) {
	tCtx, tCancel := context.WithTimeout(ctx, 10*time.Second)
	defer tCancel()
	groups := cliutil.CommandGroups{}
	err := cliutil.WithNetwork(tCtx, func(ctx context.Context, _ daemon.DaemonClient) error {
		return cliutil.WithStartedConnector(ctx, false, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
			remote, err := connectorClient.ListCommands(ctx, &empty.Empty{})
			if err != nil {
				return fmt.Errorf("unable to call ListCommands: %w", err)
			}
			groups, err = cliutil.RPCToCommands(remote, runRemote)
			return err
		})
	})
	if err != nil {
		return nil, err
	}
	return groups, nil
}

func runRemote(cmd *cobra.Command, args []string) error {
	return cliutil.WithNetwork(cmd.Context(), func(ctx context.Context, _ daemon.DaemonClient) error {
		return cliutil.WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
			result, err := connectorClient.RunCommand(ctx, &connector.RunCommandRequest{OsArgs: os.Args[1:]})
			if err != nil {
				return err
			}
			cmd.OutOrStdout().Write(result.GetStdout())
			cmd.ErrOrStderr().Write(result.GetStderr())
			return nil
		})
	})
}

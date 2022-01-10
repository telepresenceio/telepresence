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
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/extensions"
	empty "google.golang.org/protobuf/types/known/emptypb"
)

func getRemoteCommands(ctx context.Context) (CommandGroups, error) {
	tCtx, tCancel := context.WithTimeout(ctx, 10*time.Second)
	defer tCancel()
	groups := CommandGroups{}
	err := cliutil.WithNetwork(tCtx, func(ctx context.Context, _ daemon.DaemonClient) error {
		return cliutil.WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
			remote, err := connectorClient.ListCommands(ctx, &empty.Empty{})
			for name, cmds := range remote.GetCommandGroups() {
				commands := []*cobra.Command{}
				for _, cmd := range cmds.GetCommands() {
					cobraCmd := &cobra.Command{
						Use:   cmd.GetName(),
						Long:  cmd.GetLongHelp(),
						Short: cmd.GetShortHelp(),
						RunE:  runRemote,
					}
					for _, flag := range cmd.GetFlags() {
						tp, err := extensions.TypeFromString(flag.GetType())
						if err != nil {
							return err
						}
						val, err := tp.NewFlagValue([]byte{})
						if err != nil {
							return err
						}
						cobraCmd.Flags().Var(val, flag.GetFlag(), flag.GetHelp())
					}
					commands = append(commands, cobraCmd)
				}
				groups[name] = commands
			}
			if err != nil {
				return fmt.Errorf("unable to call ListCommands: %w", err)
			}
			return nil
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
			cmd.OutOrStdout().Write([]byte(result.GetStdout()))
			cmd.ErrOrStderr().Write([]byte(result.GetStderr()))
			return nil
		})
	})
}

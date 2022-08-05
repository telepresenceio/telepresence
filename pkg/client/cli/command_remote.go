package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dcontext"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/commands"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func getRemoteCommands(ctx context.Context) (groups cliutil.CommandGroups, err error) {
	err = cliutil.WithStartedConnector(ctx, false, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		remote, err := connectorClient.ListCommands(ctx, &empty.Empty{})
		if err != nil {
			return fmt.Errorf("unable to call ListCommands: %w", err)
		}
		var funcBundle = cliutil.CommandFuncBundle{
			RunE:              runRemote,
			ValidArgsFunction: validArgsFuncRemote,
		}
		if groups, err = cliutil.RPCToCommands(remote, funcBundle); err != nil {
			groups = commands.GetCommandsForLocal(ctx, err)
		}
		userDaemonRunning = true
		return nil
	})
	if err != nil && err != cliutil.ErrNoUserDaemon {
		return nil, err
	}
	if !userDaemonRunning {
		groups = commands.GetCommandsForLocal(ctx, err)
	}
	return groups, nil
}

func validArgsFuncRemote(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	var (
		resp *connector.ValidArgsForCommandResponse
		err  error
	)

	err = cliutil.WithNetwork(cmd.Context(), func(ctx context.Context, _ daemon.DaemonClient) error {
		return cliutil.WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
			resp, err = connectorClient.ValidArgsForCommand(ctx, &connector.ValidArgsForCommandRequest{
				CmdName:    cmd.Name(),
				OsArgs:     args,
				ToComplete: toComplete,
			})

			return err
		})
	})

	if err != nil {
		return []string{}, 0
	}

	return resp.Completions, cobra.ShellCompDirective(resp.ShellCompDirective)
}

func runRemote(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return cliutil.WithNetwork(cmd.Context(), func(ctx context.Context, _ daemon.DaemonClient) error {
		return cliutil.WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
			ctx, cancel := context.WithCancel(dcontext.WithSoftness(ctx))

			// Ensure that appropriate signals terminates the context.
			var sigCh = make(chan os.Signal, 1)
			signal.Notify(sigCh, proc.SignalsToForward...)
			defer func() {
				signal.Stop(sigCh)
				close(sigCh)
			}()
			go func() {
				select {
				case <-ctx.Done():
				case sig := <-sigCh:
					if sig == nil {
						return
					}
					cancel()
				}
			}()

			result, err := connectorClient.RunCommand(ctx, &connector.RunCommandRequest{
				// FlagParsing is disabled on the local-side cmd so args is actually going to hold flags and args both
				// Thus command_name + args is the entire command line (except for the "telepresence" string in os.Args[0])
				OsArgs: append([]string{cmd.CalledAs()}, args...),
				Cwd:    cwd,
			})
			if err != nil {
				if s, ok := status.FromError(err); ok {
					if s.Code() == codes.Canceled {
						err = nil
					} else {
						err = errors.New(s.Message())
					}
				}
				return err
			}

			_, _ = cmd.OutOrStdout().Write(result.GetStdout())
			_, _ = cmd.ErrOrStderr().Write(result.GetStderr())

			return nil
		})
	})
}

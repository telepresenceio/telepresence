package main

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func main() {
	ctx := context.Background()
	if dir := os.Getenv("DEV_TELEPRESENCE_CONFIG_DIR"); dir != "" {
		ctx = filelocation.WithAppUserConfigDir(ctx, dir)
	}
	if dir := os.Getenv("DEV_TELEPRESENCE_LOG_DIR"); dir != "" {
		ctx = filelocation.WithAppUserLogDir(ctx, dir)
	}

	var cmd *cobra.Command
	if len(os.Args) > 1 && os.Args[1] == "daemon-foreground" || len(os.Args) > 2 && os.Args[2] == "daemon-foreground" && os.Args[1] == "help" {
		// Avoid the initialization of all subcommands except for daemon-foreground an
		// avoids checks for legacy commands.
		cmd = &cobra.Command{
			Use:  "telepresence",
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SetOut(cmd.ErrOrStderr())
				return nil
			},
			SilenceErrors: true, // main() will handle it after .ExecuteContext() returns
			SilenceUsage:  true, // our FlagErrorFunc will handle it
			// BUG(lukeshu): This doesn't have FlagErrorFunc wired up
		}
		cmd.AddCommand(daemon.Command())
	} else {
		cmd = cli.Command(ctx)
		cmd.AddCommand(connector.Command())
	}

	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %v\n", cmd.CommandPath(), err)
		summarizeLogs(ctx, cmd)
		os.Exit(1)
	}
}

func summarizeLogs(ctx context.Context, cmd *cobra.Command) {
	daemonLogs, err := logging.SummarizeLog(ctx, daemon.ProcessName)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %+v\n", cmd.CommandPath(), err)
	}
	connectorLogs, err := logging.SummarizeLog(ctx, connector.ProcessName)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %+v\n", cmd.CommandPath(), err)
	}

	if daemonLogs != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "\n%s", daemonLogs)
	}
	if connectorLogs != "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "\n%s", connectorLogs)
	}
}

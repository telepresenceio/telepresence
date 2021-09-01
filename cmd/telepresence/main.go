package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
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

	env, err := client.LoadEnv(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load environment: %v", err)
		os.Exit(1)
	}
	ctx = client.WithEnv(ctx, env)

	var cmd *cobra.Command
	if isDaemon() {
		// Avoid the initialization of all subcommands except for [connector|daemon]-foreground and
		// avoids checks for legacy commands.
		cmd = &cobra.Command{
			Use:  "telepresence",
			Args: cli.OnlySubcommands,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SetOut(cmd.ErrOrStderr())
				return nil
			},
			SilenceErrors: true, // main() will handle it after .ExecuteContext() returns
			SilenceUsage:  true, // our FlagErrorFunc will handle it
		}
		cmd.AddCommand(connector.Command())
		cmd.AddCommand(daemon.Command())
		if err := cmd.ExecuteContext(ctx); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %v\n", cmd.CommandPath(), err)
			os.Exit(1)
		}
	} else {
		cfg, err := client.LoadConfig(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to load config: %v", err)
			os.Exit(1)
		}
		ctx = client.WithConfig(ctx, cfg)
		cmd = cli.Command(ctx)
		if err := cmd.ExecuteContext(ctx); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %v\n", cmd.CommandPath(), err)
			summarizeLogs(ctx, cmd)
			os.Exit(1)
		}
	}
}

func isDaemon() bool {
	const fg = "-foreground"
	a := os.Args
	return len(a) > 1 && strings.HasSuffix(a[1], fg) || len(a) > 2 && strings.HasSuffix(a[2], fg) && a[1] == "help"
}

func summarizeLogs(ctx context.Context, cmd *cobra.Command) {
	w := cmd.ErrOrStderr()
	first := true
	for _, proc := range []string{daemon.ProcessName, connector.ProcessName} {
		if summary, err := logging.SummarizeLog(ctx, proc); err != nil {
			fmt.Fprintf(w, "failed to scan %s logs: %v\n", proc, err)
		} else if summary != "" {
			if first {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, summary)
		}
	}
}

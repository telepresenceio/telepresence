package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/commands"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
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
		cmd.AddCommand(userd.Command(commands.GetCommands, []userd.DaemonService{}, []trafficmgr.SessionService{}))
		cmd.AddCommand(rootd.Command())
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
			if errcat.GetCategory(err) > errcat.NoLogs {
				summarizeLogs(ctx, cmd)
				// If the user gets here, it might be an actual bug that they found, so
				// point them to the `gather-logs` command in case they want to open an
				// issue.
				fmt.Fprintln(cmd.ErrOrStderr(), "If you think you have encountered a bug"+
					", please run `telepresence gather-logs` and attach the "+
					"telepresence_logs.zip to your github issue or create a new one: "+
					"https://github.com/telepresenceio/telepresence/issues/new?template=Bug_report.md .")
			}
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
	for _, proc := range []string{rootd.ProcessName, userd.ProcessName} {
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

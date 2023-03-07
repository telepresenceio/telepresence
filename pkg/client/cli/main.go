package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/util"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	userDaemon "github.com/telepresenceio/telepresence/v2/pkg/client/userd/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func InitContext(ctx context.Context) context.Context {
	env, err := client.LoadEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load environment: %v", err)
		os.Exit(1)
	}
	ctx = client.WithEnv(ctx, env)
	switch client.ProcessName() {
	case userd.ProcessName:
		if proc.RunningInContainer() {
			client.DisplayName = "OSS Daemon in container"
		} else {
			client.DisplayName = "OSS User Daemon"
		}
		ctx = userd.WithNewServiceFunc(ctx, userDaemon.NewService)
		ctx = userd.WithNewSessionFunc(ctx, trafficmgr.NewSession)
	case rootd.ProcessName:
		client.DisplayName = "OSS Root Daemon"
		ctx = rootd.WithNewServiceFunc(ctx, rootd.NewService)
		ctx = rootd.WithNewSessionFunc(ctx, rootd.NewSession)
	default:
		client.DisplayName = "OSS Client"
		ctx = util.WithCommandInitializer(ctx, util.CommandInitializer)
		ctx = WithSubCommands(ctx)
	}
	return ctx
}

func Main(ctx context.Context) {
	if dir := os.Getenv("DEV_TELEPRESENCE_CONFIG_DIR"); dir != "" {
		ctx = filelocation.WithAppUserConfigDir(ctx, dir)
	}
	if dir := os.Getenv("DEV_TELEPRESENCE_LOG_DIR"); dir != "" {
		ctx = filelocation.WithAppUserLogDir(ctx, dir)
	}

	if client.IsDaemon() {
		// Avoid the initialization of all subcommands except for [connector|daemon]-foreground and
		// avoids checks for legacy commands.
		cmd := &cobra.Command{
			Use:  "telepresence",
			Args: OnlySubcommands,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmd.SetOut(cmd.ErrOrStderr())
				return nil
			},
			SilenceErrors: true, // main() will handle it after .ExecuteContext() returns
			SilenceUsage:  true, // our FlagErrorFunc will handle it
		}
		cmd.AddCommand(userDaemon.Command())
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
		if ctx, err = logging.InitContext(ctx, "cli", logging.RotateDaily, false); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		if cmd, fmtOutput, err := output.Execute(Command(ctx)); err != nil {
			if fmtOutput {
				os.Exit(1)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: error: %v\n", cmd.CommandPath(), err)
			if errcat.GetCategory(err) > errcat.NoDaemonLogs {
				if summarizeLogs(ctx, cmd) {
					// If the user gets here, it might be an actual bug that they found, so
					// point them to the `gather-logs` command in case they want to open an
					// issue.
					fmt.Fprintln(cmd.ErrOrStderr(), "If you think you have encountered a bug"+
						", please run `telepresence gather-logs` and attach the "+
						"telepresence_logs.zip to your github issue or create a new one: "+
						"https://github.com/telepresenceio/telepresence/issues/new?template=Bug_report.md .")
				}
			}
			os.Exit(1)
		}
	}
}

// summarizeLogs outputs the logs from the root and user daemons. It returns true
// if output were produced, false otherwise (might happen if no logs exist yet).
func summarizeLogs(ctx context.Context, cmd *cobra.Command) bool {
	w := cmd.ErrOrStderr()
	first := true
	for _, proc := range []string{rootd.ProcessName, userd.ProcessName} {
		if summary, err := logging.SummarizeLog(ctx, proc); err != nil {
			fmt.Fprintf(w, "failed to scan %s logs: %v\n", proc, err)
		} else if summary != "" {
			if first {
				fmt.Fprintln(w)
				first = false
			}
			fmt.Fprintln(w, summary)
		}
	}
	return !first
}

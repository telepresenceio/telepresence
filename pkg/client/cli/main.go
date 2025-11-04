package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cmd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func InitContext(ctx context.Context) context.Context {
	env, err := client.LoadEnv()
	if err != nil {
		ioutil.Printf(os.Stderr, "Failed to load environment: %v", err)
		os.Exit(1)
	}
	ctx = client.WithEnv(ctx, env)
	switch client.ProcessName() {
	case client.UserDaemonName:
		client.DisplayName = "OSS User Daemon"
		if proc.RunningInContainer() {
			if slices.Contains(os.Args, "--embed-network") {
				client.DisplayName = "OSS Daemon in container"
			} else {
				// False positive, likely due to a /.dockerenv file in CodesSpace
				proc.SetRunningInContainer(false)
			}
		}
	case client.RootDaemonName:
		client.DisplayName = "OSS Root Daemon"
		proc.SetRunningInContainer(false) // We never start the root daemon as a container.
	default:
		client.DisplayName = "OSS Client"
	}
	if client.IsDaemon() {
		ctx = cmd.WithDaemonSubCommands(ctx)
	} else {
		ctx = cmd.WithSubCommands(ctx)
	}
	return ctx
}

func Main(ctx context.Context, args []string) {
	if dir := os.Getenv("DEV_TELEPRESENCE_CONFIG_DIR"); dir != "" {
		ctx = filelocation.WithAppUserConfigDir(ctx, dir)
	}
	if dir := os.Getenv("DEV_TELEPRESENCE_LOG_DIR"); dir != "" {
		ctx = filelocation.WithAppUserLogDir(ctx, dir)
	}

	if client.IsDaemon() {
		// Avoid the initialization of all subcommands except for [userd|rootd|kubeauthd] and
		// avoids checks for legacy commands.
		if command, _, err := output.Execute(cmd.TelepresenceDaemon(ctx, args)); err != nil {
			if command != nil {
				ioutil.Printf(command.ErrOrStderr(), "%s: error: %v\n", command.CommandPath(), err)
			}
			os.Exit(1)
		}
	} else {
		if command, fmtOutput, err := output.Execute(cmd.Telepresence(ctx, args)); err != nil {
			if fmtOutput || errcat.GetCategory(err) == errcat.Silent {
				exitCode := 1
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					exitCode = exitErr.ExitCode()
				}
				os.Exit(exitCode)
			}
			if command != nil {
				ioutil.Printf(command.ErrOrStderr(), "%s: error: %v\n", command.CommandPath(), err)
				if errcat.GetCategory(err) > errcat.NoDaemonLogs {
					if summarizeLogs(ctx, command) {
						// If the user gets here, it might be an actual bug that they found, so
						// point them to the `gather-logs` command in case they want to open an
						// issue.
						ioutil.Println(command.ErrOrStderr(), "If you think you have encountered a bug"+
							", please run `telepresence gather-logs` and attach the "+
							"telepresence_logs.zip to your github issue or create a new one: "+
							"https://github.com/telepresenceio/telepresence/issues/new?template=Bug_report.md .")
					}
				}
			} else {
				ioutil.Printf(os.Stderr, "%v\n", err)
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
	for _, processName := range []string{client.RootDaemonName, client.UserDaemonName} {
		if summary, err := logging.SummarizeLog(ctx, processName); err != nil {
			ioutil.Printf(w, "failed to scan %s logs: %v\n", processName, err)
		} else if summary != "" {
			if first {
				ioutil.Println(w)
				first = false
			}
			ioutil.Println(w, summary)
		}
	}
	return !first
}

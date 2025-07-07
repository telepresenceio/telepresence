package cmd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	cliDocker "github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func dockerRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "docker-run",
		Short: "Docker run with daemon network",
		Args:  cobra.ArbitraryArgs,
		Annotations: map[string]string{
			ann.Session: ann.Optional,
		},
		RunE:                  runDockerRunCLI,
		SilenceErrors:         true,
		SilenceUsage:          true,
		DisableFlagParsing:    true,
		DisableFlagsInUseLine: true,
		ValidArgsFunction:     cliDocker.AutocompleteRun,
	}
	return cmd
}

func findAndParseFlag(flags *pflag.FlagSet, flagName string, args []string) ([]string, error) {
	if i := slices.Index(args, "--"+flagName); i >= 0 && i+1 < len(args) {
		if err := flags.Parse(args[i : i+2]); err != nil {
			return nil, err
		}
		args = slices.Delete(args, i, i+2)
	} else if i = slices.IndexFunc(args, func(s string) bool { return strings.HasPrefix(s, "--"+flagName+"=") }); i >= 0 {
		if err := flags.Parse(args[i : i+1]); err != nil {
			return nil, err
		}
		args = slices.Delete(args, i, i+1)
	}
	return args, nil
}

func parseFlags(cmd *cobra.Command, args []string) ([]string, error) {
	// The command has all flag parsing disabled, but we must check for the global flags. Luckily, these flags do not conflict with
	// the docker run flags.
	opts := cmd.Flags()
	var err error
	for _, n := range global.FlagNames {
		args, err = findAndParseFlag(opts, n, args)
		if err != nil {
			return nil, err
		}
	}
	return args, nil
}

func runDockerRunCLI(cmd *cobra.Command, args []string) error {
	return errcat.NoDaemonLogs.New(runDockerRun(cmd, args))
}

func runDockerRun(cmd *cobra.Command, args []string) error {
	args, err := parseFlags(cmd, args)
	if err != nil {
		return err
	}
	if slices.Contains(args, "--help") {
		return proc.StdCommand(cmd.Context(), docker.Exe, slices.Insert(args, 0, "run")...).Run()
	}

	err = connect.InitCommand(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	ud := daemon.GetUserClient(ctx)
	if ud == nil {
		return fmt.Errorf("%s requires a connection", cmd.UseLine())
	}
	if !ud.Containerized() {
		return fmt.Errorf("%s requires that --docker was used when the connection was established", cmd.UseLine())
	}
	cni, cc, err := docker.Start(ctx, true, args...)
	if err != nil {
		return errcat.NoDaemonLogs.New(err)
	}
	if cc == nil {
		// Container already exited
		return nil
	}
	progress.Write(ctx, progress.DoneEvent(cni.Name, fmt.Sprintf("Started container %s with IP %s", cni.Name, cni.IP)))

	var exited, signalled atomic.Bool
	done := make(chan error, 1)
	if flags.HasOption("tty", 't', args) {
		close(done)
	} else {
		go cliDocker.EnsureStopContainer(ctx, cni.Name, cni.ID, nil, &exited, &signalled, done)
	}

	err = cc.Wait()
	exited.Store(true)
	cancel()
	if signalled.Load() {
		err = nil
	}
	waitErr := <-done
	if err == nil {
		err = waitErr
	}
	return errcat.NoDaemonLogs.New(err)
}

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/containerd/errdefs"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	cliDocker "github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/global"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/progress"
	"github.com/telepresenceio/telepresence/v2/pkg/client/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
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
		return proc.StdCommand(cmd.Context(), cliDocker.Exe, slices.Insert(args, 0, "run")...).Run()
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
	cidFileName, err := ioutil.CreateTempName("", "docker-run*.cid")
	if err != nil {
		return err
	}

	dockerOpts := []string{"run", "--cidfile", cidFileName}
	dns, nwName, err := cliDocker.GetDaemonContainerNetworkInfo(ctx)
	if err != nil {
		return err
	}
	dockerOpts = append(dockerOpts, "--dns", dns.String())
	if nwName != "" {
		dockerOpts = append(dockerOpts, "--network", nwName)
	}

	ctx = dos.WithStdio(ctx, cmd)
	cc := proc.StdCommand(ctx, cliDocker.Exe, slices.Insert(args, 0, dockerOpts...)...)
	cc.Stdin = dos.Stdin(ctx)
	cc.Env = dos.Environ(ctx)
	tty := flags.HasOption("tty", 't', args)
	if !tty {
		proc.CreateNewProcessGroup(cc.Cmd)
	}

	defer func() {
		_ = os.Remove(cidFileName)
	}()

	err = cc.Start()
	if err != nil {
		return err
	}

	containerID, err := cliDocker.ReadContainerID(ctx, cidFileName)
	if err != nil {
		// Process didn't produce a cidfile, so the container failed to start
		if !errors.Is(err, fs.ErrNotExist) {
			dlog.Error(ctx, err)
		}
		return cc.Wait()
	}

	ctx = docker.EnableClient(ctx)
	cni, err := docker.GetContainerInfo(ctx, containerID, nwName)
	if err != nil {
		if errdefs.IsNotFound(err) {
			// Container is already done, so not much left to do here.
			cancel()
			err = cc.Wait()
		}
		return errcat.NoDaemonLogs.New(err)
	}
	progress.Write(ctx, progress.DoneEvent(cni.Name, fmt.Sprintf("Started container %s with IP %s", cni.Name, cni.IP)))

	var exited, signalled atomic.Bool
	done := make(chan error, 1)
	if tty {
		close(done)
	} else {
		go cliDocker.EnsureStopContainer(ctx, cni.Name, containerID, nil, &exited, &signalled, done)
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

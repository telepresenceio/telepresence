package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync/atomic"

	"github.com/spf13/cobra"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	cliDocker "github.com/telepresenceio/telepresence/v2/pkg/client/cli/docker"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/flags"
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

func runDockerRunCLI(cmd *cobra.Command, args []string) error {
	return errcat.NoDaemonLogs.New(runDockerRun(cmd, args))
}

func runDockerRun(cmd *cobra.Command, args []string) error {
	if slices.Contains(args, "--help") {
		return proc.StdCommand(cmd.Context(), cliDocker.Exe, slices.Insert(args, 0, "run")...).Run()
	}

	err := connect.InitCommand(cmd)
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
		return err
	}

	ctx = docker.EnableClient(ctx)
	cni, err := docker.GetContainerInfo(ctx, containerID, nwName)
	if err != nil {
		return err
	}
	progress.Write(ctx, progress.DoneEvent(cni.Name, fmt.Sprintf("Started container %s with IP %s", cni.Name, cni.IP)))

	var exited, signalled atomic.Bool
	done := make(chan error, 1)
	if tty {
		close(done)
	} else {
		go cliDocker.EnsureStopContainer(ctx, containerID, nil, &exited, &signalled, done)
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

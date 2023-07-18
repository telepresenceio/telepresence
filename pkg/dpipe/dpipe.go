package dpipe

import (
	"context"
	"fmt"
	"io"
	"os/exec" //nolint:depguard // We want no logging and no soft-context signal handling

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

func DPipe(ctx context.Context, peer io.ReadWriteCloser, cmdName string, cmdArgs ...string) error {
	defer func() {
		_ = peer.Close()
	}()

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)
	cmd.Stdin = peer
	cmd.Stdout = peer
	cmd.Stderr = dlog.StdLogger(ctx, dlog.LogLevelError).Writer()

	cmdLine := shellquote.ShellString(cmd.Path, cmd.Args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", cmdLine, err)
	}

	ctx = dlog.WithField(ctx, "exec.pid", cmd.Process.Pid)
	dlog.Infof(ctx, "started command %s", cmdLine)
	defer dlog.Infof(ctx, "ended command %s", cmdName)
	runFinished := make(chan error)
	go func() {
		defer close(runFinished)
		if err := cmd.Wait(); err != nil {
			if !cmd.ProcessState.Success() && ctx.Err() == nil {
				runFinished <- err
			}
		}
	}()

	select {
	case <-ctx.Done():
		killProcess(ctx, cmd)
		return nil
	case err := <-runFinished:
		return err
	}
}

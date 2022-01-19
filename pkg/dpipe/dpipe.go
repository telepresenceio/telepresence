package dpipe

import (
	"context"
	"fmt"
	"io"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

func DPipe(ctx context.Context, peer io.ReadWriteCloser, cmdName string, cmdArgs ...string) error {
	defer func() {
		_ = peer.Close()
	}()

	cmd := dexec.CommandContext(ctx, cmdName, cmdArgs...)
	cmd.Stdin = peer
	cmd.Stdout = peer
	cmd.Stderr = dlog.StdLogger(ctx, dlog.LogLevelError).Writer()
	cmd.DisableLogging = true // Avoid data logging (peer is not a *os.File)

	cmdLine := shellquote.ShellString(cmd.Path, cmd.Args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", cmdLine, err)
	}

	ctx = dlog.WithField(ctx, "dexec.pid", cmd.Process.Pid)
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

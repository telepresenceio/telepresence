package dpipe

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/shellquote"
)

func DPipe(ctx context.Context, peer io.ReadWriteCloser, cmdName string, cmdArgs ...string) error {
	// Under normal operation, waitCloseAndKill is going to clean up the running sshfs command
	// By passing in a hard context here, we make sure that dexec does an ungraceful kill if it ever
	// turns out that waitCloseAndKill wasn't able to kill the sshfs and the daemon is shutting down in a hurry.
	hardCtx := dcontext.HardContext(ctx)
	cmd := dexec.CommandContext(hardCtx, cmdName, cmdArgs...)
	cmd.Stdin = peer
	cmd.Stdout = peer
	cmd.Stderr = io.Discard   // Ensure error logging by passing a non nil, non *os.File here
	cmd.DisableLogging = true // Avoid data logging (peer is not a *os.File)

	cmdLine := shellquote.ShellString(cmd.Path, cmd.Args)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", cmdLine, err)
	}

	var killTimer *time.Timer
	closing := int32(0)

	defer func() {
		if killTimer != nil && atomic.LoadInt32(&closing) == 1 {
			killTimer.Stop()
		}
	}()

	go waitCloseAndKill(ctx, cmd, peer, &closing, &killTimer)

	ctx = dlog.WithField(ctx, "dexec.pid", cmd.Process.Pid)
	dlog.Infof(ctx, "started command %s", cmdLine)
	err := cmd.Wait()
	how := "successfully"
	if err != nil {
		if cmd.ProcessState.Success() {
			// Error is most likely "use of closed connection", which is normal for pipes
			dlog.Debugf(ctx, "normal exit caused by: %v", err)
			err = nil
		} else if ctx.Err() != nil {
			how = "by cancellation"
			err = nil
		}
	}
	if err != nil {
		how = "with error"
	}
	dlog.Infof(ctx, "finished %s: %v", how, cmd.ProcessState)
	return err
}

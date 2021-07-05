package dpipe

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

func DPipe(ctx context.Context, cmd *dexec.Cmd, peer io.ReadWriteCloser) error {
	cmdOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to establish stdout pipe: %w", err)
	}
	defer cmdOut.Close()

	cmdIn, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to establish stdin pipe: %w", err)
	}
	defer cmdIn.Close()

	if err = cmd.Start(); err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}

	var killTimer *time.Timer
	closing := int32(0)

	defer func() {
		if killTimer != nil && atomic.LoadInt32(&closing) == 1 {
			killTimer.Stop()
		}
	}()

	go waitCloseAndKill(ctx, cmd, peer, &closing, &killTimer)

	go func() {
		if _, err := io.Copy(cmdIn, peer); err != nil && atomic.LoadInt32(&closing) == 0 {
			dlog.Errorf(ctx, "copy from sftp-server to connection failed: %v", err)
		}
	}()

	go func() {
		if _, err := io.Copy(peer, cmdOut); err != nil && atomic.LoadInt32(&closing) == 0 {
			dlog.Errorf(ctx, "copy from connection to sftp-server failed: %v", err)
		}
	}()
	if err = cmd.Wait(); err != nil && atomic.LoadInt32(&closing) == 0 {
		return fmt.Errorf("execution failed: %w", err)
	}
	return nil
}

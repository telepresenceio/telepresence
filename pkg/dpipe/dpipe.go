package dpipe

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

func DPipe(ctx context.Context, cmd *dexec.Cmd, peer io.ReadWriteCloser) error {
	cmdOut, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to establish stdout pipe: %v", err)
	}
	defer cmdOut.Close()

	cmdIn, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to establish stdin pipe: %v", err)
	}
	defer cmdIn.Close()

	if err = cmd.Start(); err != nil {
		return fmt.Errorf("failed to start: %v", err)
	}

	var killTimer *time.Timer
	closing := int32(0)
	defer func() {
		if atomic.LoadInt32(&closing) == 1 {
			killTimer.Stop()
		}
	}()

	go func() {
		<-ctx.Done()
		// A process is sometimes not terminated gracefully by the SIGTERM, so we give
		// it a second to succeed and then kill it forcefully.
		killTimer = time.AfterFunc(time.Second, func() {
			_ = cmd.Process.Signal(unix.SIGKILL)
		})
		atomic.StoreInt32(&closing, 1)
		_ = peer.Close()
		_ = cmd.Process.Signal(unix.SIGTERM)
	}()

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
		return fmt.Errorf("execution failed: %v", err)
	}
	return nil
}

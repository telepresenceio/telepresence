package dpipe

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
)

func DPipe(ctx context.Context, cmd *dexec.Cmd, peer io.ReadWriteCloser) error {
	cmd.Stdin = peer
	cmd.Stdout = peer
	cmd.DisableLogging = true

	dlog.Debugf(ctx, "Starting %s", logging.ShellString(cmd.Path, cmd.Args))
	if err := cmd.Start(); err != nil {
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
	if err := cmd.Wait(); err != nil && atomic.LoadInt32(&closing) == 0 {
		return fmt.Errorf("execution failed: %w", err)
	}
	return nil
}

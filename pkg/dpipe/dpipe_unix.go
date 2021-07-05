// +build !windows

package dpipe

import (
	"context"
	"io"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
)

func waitCloseAndKill(ctx context.Context, cmd *dexec.Cmd, peer io.Closer, closing *int32, killTimer **time.Timer) {
	<-ctx.Done()

	// A process is sometimes not terminated gracefully by the SIGTERM, so we give
	// it a second to succeed and then kill it forcefully.
	*killTimer = time.AfterFunc(time.Second, func() {
		_ = cmd.Process.Signal(unix.SIGKILL)
	})
	atomic.StoreInt32(closing, 1)

	_ = peer.Close()
	_ = cmd.Process.Signal(os.Interrupt)
}

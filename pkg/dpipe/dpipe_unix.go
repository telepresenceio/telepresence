//go:build !windows
// +build !windows

package dpipe

import (
	"context"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"github.com/datawire/dlib/dexec"
)

func killProcess(_ context.Context, cmd *dexec.Cmd) {
	// A process is sometimes not terminated gracefully by the SIGTERM, so we give
	// it a second to succeed and then kill it forcefully.
	time.AfterFunc(time.Second, func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Signal(unix.SIGKILL)
		}
	})
	_ = cmd.Process.Signal(os.Interrupt)
}

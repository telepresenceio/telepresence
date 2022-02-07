//go:build !windows
// +build !windows

package dpipe

import (
	"context"
	"os"
	"time"

	//nolint:depguard // We want no logging and no soft-context signal handling
	"os/exec"

	"golang.org/x/sys/unix"
)

func killProcess(_ context.Context, cmd *exec.Cmd) {
	// A process is sometimes not terminated gracefully by the SIGTERM, so we give
	// it a second to succeed and then kill it forcefully.
	time.AfterFunc(time.Second, func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Signal(unix.SIGKILL)
		}
	})
	_ = cmd.Process.Signal(os.Interrupt)
}

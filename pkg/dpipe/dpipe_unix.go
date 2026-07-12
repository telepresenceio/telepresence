//go:build !windows

package dpipe

import (
	"context"
	"os"
	"os/exec" //nolint:depguard // We want no logging and no soft-context signal handling
	"time"

	"golang.org/x/sys/unix"
)

func killProcess(_ context.Context, cmd *exec.Cmd) {
	// A process is sometimes not terminated gracefully by the SIGTERM, so we give
	// it some time to succeed and then kill it forcefully. The time must allow
	// for a FUSE process to unmount before it exits; with a kext-less FUSE
	// implementation (FUSE-T), the kernel NFS mount outlives a killed process.
	time.AfterFunc(5*time.Second, func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Signal(unix.SIGKILL)
		}
	})
	_ = cmd.Process.Signal(os.Interrupt)
}

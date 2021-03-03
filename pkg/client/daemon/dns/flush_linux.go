package dns

import (
	"context"
	"os/exec"
)

// Flush makes an attempt to flush the host's DNS cache
func Flush(c context.Context) {
	// GNU libc Name Service Cache Daemon
	_ = exec.Command("nscd", "--invalidate=hosts").Run()

	// systemd-resolved
	_ = exec.Command("resolvectl", "flush-caches").Run()
}

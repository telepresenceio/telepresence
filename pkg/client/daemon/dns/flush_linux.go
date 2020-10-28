package dns

import (
	"os/exec"
)

func Flush() {
	// GNU libc Name Service Cache Daemon
	_ = exec.Command("nscd", "--invalidate=hosts").Run()

	// systemd-resolved
	_ = exec.Command("resolvectl", "flush-caches").Run()
}

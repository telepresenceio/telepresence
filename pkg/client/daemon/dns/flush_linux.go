package dns

import (
	"context"

	"github.com/datawire/dlib/dexec"
)

// Flush makes an attempt to flush the host's DNS cache
func Flush(c context.Context) {
	// GNU libc Name Service Cache Daemon
	_ = dexec.CommandContext(c, "nscd", "--invalidate=hosts").Run()

	// systemd-resolved
	_ = dexec.CommandContext(c, "resolvectl", "flush-caches").Run()
}

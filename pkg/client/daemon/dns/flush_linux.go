package dns

import (
	"context"

	"github.com/datawire/dlib/dexec"
)

// Flush makes an attempt to flush the host's DNS cache
func Flush(ctx context.Context) {
	// GNU libc Name Service Cache Daemon
	_ = dexec.CommandContext(ctx, "nscd", "--invalidate=hosts").Run()

	// systemd-resolved
	_ = dexec.CommandContext(ctx, "resolvectl", "flush-caches").Run()
}

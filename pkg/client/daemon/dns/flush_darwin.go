package dns

import (
	"context"

	"github.com/datawire/dlib/dexec"
)

func Flush(c context.Context) {
	// As of macOS 11 (Big Sur), how to flush the DNS cache hasn't changed since 10.10.4 (a Yosemite version
	// released mid 2015).
	_ = dexec.CommandContext(c, "killall", "-HUP", "mDNSResponder").Run()
	_ = dexec.CommandContext(c, "killall", "mDNSResponderHelper").Run() // Needs to be killed since MacOS 12. Restarts automatically
	_ = dexec.CommandContext(c, "dscacheutil", "-flushcache").Run()
}

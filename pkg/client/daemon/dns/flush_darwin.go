package dns

import (
	"context"

	"github.com/datawire/dlib/dexec"
)

func Flush(c context.Context) {
	// As of macOS 11 (Big Sur), how to flush the DNS cache hasn't changed since 10.10.4 (a Yosemite version release in mid 2015),
	// other than that in 10.12 (Sierra) it became necessary to also kill mDNSResponderHelper. On older versions the call to kill
	// mDNSResponderHelper is unnecessary but harmless, as the process doesn't exist.
	_ = dexec.CommandContext(c, "killall", "-HUP", "mDNSResponder").Run()
	_ = dexec.CommandContext(c, "killall", "mDNSResponderHelper").Run()
	_ = dexec.CommandContext(c, "dscacheutil", "-flushcache").Run()
}

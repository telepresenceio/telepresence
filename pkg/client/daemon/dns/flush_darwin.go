package dns

import (
	"os/exec"
)

func Flush() {
	// As of macOS 11 (Big Sur), how to flush the DNS cache hasn't changed since 10.10.4 (a Yosemite version
	// released mid 2015).
	_ = exec.Command("dscacheutil", "-flushcache").Run()
	_ = exec.Command("killall", "-HUP", "mDNSResponder").Run()
}

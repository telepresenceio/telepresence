package main

import (
	"os"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/teleproxy"
)

// RunAsTeleproxyIntercept is the main function when executing as
// teleproxy intercept
func RunAsTeleproxyIntercept() error {
	if os.Geteuid() != 0 {
		return errors.New("edgectl daemon as teleproxy intercept must run as root")
	}
	tele := &teleproxy.Teleproxy{Mode: "intercept"}
	return teleproxy.RunTeleproxy(tele, displayVersion)
}

// RunAsTeleproxyBridge is the main function when executing as
// teleproxy bridge
func RunAsTeleproxyBridge() error {
	tele := &teleproxy.Teleproxy{Mode: "bridge"}
	return teleproxy.RunTeleproxy(tele, displayVersion)
}

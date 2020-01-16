// +build !windows

package main

import (
	"os"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/pkg/teleproxy"
)

// RunAsTeleproxyIntercept is the main function when executing as
// teleproxy intercept
func RunAsTeleproxyIntercept(dns, fallback string) error {
	if os.Geteuid() != 0 {
		return errors.New("edgectl daemon as teleproxy intercept must run as root")
	}
	tele := &teleproxy.Teleproxy{
		Mode:       "intercept",
		DNSIP:      dns,
		FallbackIP: fallback,
	}
	return teleproxy.RunTeleproxy(tele, displayVersion)
}

// RunAsTeleproxyBridge is the main function when executing as
// teleproxy bridge
func RunAsTeleproxyBridge(context, namespace string) error {
	tele := &teleproxy.Teleproxy{
		Mode:      "bridge",
		Context:   context,
		Namespace: namespace,
	}
	return teleproxy.RunTeleproxy(tele, displayVersion)
}

// +build !windows

package teleproxy

import (
	"os"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/teleproxy"
)

// RunAsIntercept is the main function when executing as
// teleproxy intercept
func RunAsIntercept(dns, fallback string) error {
	if os.Geteuid() != 0 {
		return errors.New("edgectl daemon as teleproxy intercept must run as root")
	}
	tele := &teleproxy.Teleproxy{
		Mode:       "intercept",
		DNSIP:      dns,
		FallbackIP: fallback,
	}
	return teleproxy.RunTeleproxy(tele, edgectl.DisplayVersion())
}

// RunAsBridge is the main function when executing as
// teleproxy bridge
func RunAsBridge(context, namespace string) error {
	tele := &teleproxy.Teleproxy{
		Mode:      "bridge",
		Context:   context,
		Namespace: namespace,
	}
	return teleproxy.RunTeleproxy(tele, edgectl.DisplayVersion())
}

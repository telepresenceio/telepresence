// +build !windows

package teleproxy

import (
	"os"

	"github.com/datawire/telepresence2/pkg/common"
	"github.com/pkg/errors"
)

// RunAsIntercept is the main function when executing as
// teleproxy intercept
func RunAsIntercept(dns, fallback string) error {
	if os.Geteuid() != 0 {
		return errors.New("edgectl daemon as teleproxy intercept must run as root")
	}
	tele := &Teleproxy{
		Mode:       "intercept",
		DNSIP:      dns,
		FallbackIP: fallback,
	}
	return RunTeleproxy(tele, common.DisplayVersion())
}

// RunAsBridge is the main function when executing as
// teleproxy bridge
func RunAsBridge(context, namespace string) error {
	tele := &Teleproxy{
		Mode:      "bridge",
		Context:   context,
		Namespace: namespace,
	}
	return RunTeleproxy(tele, common.DisplayVersion())
}

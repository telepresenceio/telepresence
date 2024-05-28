//go:build linux

package proc

import (
	"context"
	"fmt"
	"os"

	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

var runningInContainer bool //nolint:gochecknoglobals // this is a constant

func init() {
	_, err := os.Stat("/.dockerenv")
	runningInContainer = err == nil
}

// RunningInContainer returns true if the current process runs from inside a docker container.
func RunningInContainer() bool {
	return runningInContainer
}

func AppendOSSpecificContainerOpts(ctx context.Context, opts []string) ([]string, error) {
	if RunningInWSL() {
		// Using host.docker.internal:host-gateway won't work for the kubeauth process, because Windows Docker Desktop
		// will assign the IP of the Windows host, not the host from where this process was started (the Linux host).
		// We'll reach that using the gateway of the default host.
		r, err := routing.DefaultRoute(ctx)
		if err != nil {
			return opts, err
		}
		opts = append(opts, "-e", fmt.Sprintf("TELEPRESENCE_KUBEAUTH_HOST=%s", r.LocalIP))
	}
	opts = append(opts, "--add-host", "host.docker.internal:host-gateway")
	return opts, nil
}

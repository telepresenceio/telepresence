//go:build !linux

package proc

import "context"

// RunningInContainer returns true if the current process runs from inside a docker container.
func RunningInContainer() bool {
	return false
}

func AppendOSSpecificContainerOpts(ctx context.Context, opts []string) ([]string, error) {
	return opts, nil
}

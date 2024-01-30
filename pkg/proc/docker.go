package proc

import (
	"os"
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

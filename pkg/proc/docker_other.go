//go:build !linux

package proc

// RunningInContainer returns true if the current process runs from inside a docker container.
func RunningInContainer() bool {
	return false
}

func SetRunningInContainer(_ bool) {
}

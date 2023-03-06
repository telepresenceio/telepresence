package proc

import "os"

// RunningInContainer returns true if the current process runs from inside a docker container.
func RunningInContainer() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

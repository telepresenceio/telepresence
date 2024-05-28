//go:build !linux

package proc

// RunningInWSL returns true if the current process is a Linux running under WSL.
func RunningInWSL() bool {
	return false
}

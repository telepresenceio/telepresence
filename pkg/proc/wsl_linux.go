//go:build linux

package proc

import (
	"os"
	"strings"
)

var runningInWSL bool //nolint:gochecknoglobals // this is a constant

func init() {
	pv, err := os.ReadFile("/proc/version")
	if err == nil {
		sv := strings.ToLower(string(pv))
		runningInWSL = strings.Contains(sv, "microsoft") && strings.Contains(sv, "wsl")
	}
}

// RunningInWSL returns true if the current process is a Linux running under WSL.
func RunningInWSL() bool {
	return runningInWSL
}

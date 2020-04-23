package edgectl

import (
	"fmt"
	"os"
)

const (
	socketName = "/var/run/edgectl.socket"
	logfile    = "/tmp/edgectl.log"
	apiVersion = 1
)

var Version = "(unknown version)"

// SetVersion sets the current version for the executable
func SetVersion(v string) {
	Version = v
}

// DisplayVersion returns a printable version for `edgectl`
func DisplayVersion() string {
	return fmt.Sprintf("v%s (api v%d)", Version, apiVersion)
}

func GetExe() string {
	executable := ""

	// Figure out our executable
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Internal error: %v", err)
		os.Exit(1)
	}
	return executable
}

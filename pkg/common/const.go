package common

import (
	"fmt"
	"os"
)

const (
	Logfile    = "/tmp/edgectl.log"
	ApiVersion = 3
)

var Version = "(unknown version)"

// SetVersion sets the current version for the executable
func SetVersion(v string) {
	Version = v
}

// DisplayVersion returns a printable version for `edgectl`
func DisplayVersion() string {
	return fmt.Sprintf("v%s (api v%d)", Version, ApiVersion)
}

// GetExe returns the name of the running executable
func GetExe() string {
	// Figure out our executable
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Internal error: %v", err)
		os.Exit(1)
	}
	return executable
}

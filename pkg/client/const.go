package client

import (
	"fmt"
	"os"
)

const (
	Logfile    = "/tmp/telepresence.log"
	ApiVersion = 3
)

// DisplayVersion returns a printable version for `telepresence`
func DisplayVersion() string {
	return fmt.Sprintf("%s (api v%d)", Version(), ApiVersion)
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

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

var exeName string

// GetExe returns the name of the running executable
func GetExe() string {
	if exeName == "" {
		// Figure out our executable
		var err error
		exeName, err = os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Internal error: %v", err)
			os.Exit(1)
		}
	}
	return exeName
}

// SetExe defines the name of the executable (for testing purposes only)
func SetExe(executable string) {
	exeName = executable
}

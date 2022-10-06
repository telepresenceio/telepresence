package client

import (
	"fmt"
	"os"
)

const (
	// APIVersion is the API version of the daemon and connector API.
	APIVersion = 3
)

// DisplayVersion returns a printable version for `telepresence`.
func DisplayVersion() string {
	return fmt.Sprintf("%s (api v%d)", Version(), APIVersion)
}

// GetExe returns the name of the running executable.
func GetExe() string {
	// Figure out our executable
	exeName, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return exeName
}

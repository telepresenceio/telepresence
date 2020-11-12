package client

import (
	"os"
	"runtime/debug"

	"github.com/datawire/telepresence2/pkg/version"
)

// Version returns the version of this executable.
func Version() string {
	// Prefer version number inserted at build
	if version.Version != "" {
		return version.Version
	}

	v := os.Getenv("TELEPRESENCE_VERSION")
	if v != "" {
		version.Version = v
		return v
	}

	// Fall back to version info from "go get"
	if i, ok := debug.ReadBuildInfo(); ok {
		version.Version = i.Main.Version
		return version.Version
	}
	return "(unknown version)"
}

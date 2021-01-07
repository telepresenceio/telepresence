package version

import (
	"os"
	"runtime/debug"
)

// Version is inserted at build using --ldflags -X
var Version string

func init() {
	// Prefer version number inserted at build using --ldflags, but if it's not set...
	if Version == "" {
		if v := os.Getenv("TELEPRESENCE_VERSION"); v != "" {
			Version = v
		} else if i, ok := debug.ReadBuildInfo(); ok {
			// Fall back to version info from "go get"
			Version = i.Main.Version
		} else {
			Version = "(unknown version)"
		}
	}
}

package version

import (
	"os"
	"runtime/debug"
)

// Version is a "vSEMVER" string, and is either populated at build-time using `--ldflags -X`, or at
// init()-time by inspecting the binary's own debug info.
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

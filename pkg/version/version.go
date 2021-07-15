package version

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"github.com/blang/semver"
)

// Version is a "vSEMVER" string, and is either populated at build-time using `--ldflags -X`, or at
// init()-time by inspecting the binary's own debug info.
var Version string

func init() {
	// Prefer version number inserted at build using --ldflags, but if it's not set...
	if Version == "" {
		if i, ok := debug.ReadBuildInfo(); ok {
			// Fall back to version info from "go get"
			Version = i.Main.Version
		} else {
			Version = "(unknown version)"
		}
		if _, err := semver.ParseTolerant(Version); err != nil {
			if Version != "(devel)" && Version != "(unknown version)" {
				// If this isn't a parsable semver (enforced by Makefile), isn't
				// "(devel)" (a special value from runtime/debug), and isn't our own
				// special "(unknown version)", then something about the toolchain
				// has clearly changed and invalidated our assumptions.  That's
				// worthy of a panic; if this is built using an unsupported
				// compiler, we can't be sure of anything.
				panic(fmt.Errorf("this binary's compiled-in version looks invalid: %w", err))
			}
			if env := os.Getenv("TELEPRESENCE_VERSION"); strings.HasPrefix(env, "v") {
				Version = env
			}
		}
	}
}

var (
	structuredInput  string
	structuredOutput semver.Version
)

// Structured is a structured semver.Version value, and and is based on Version.
//
// The reason that this parsed dynamically instead of once at init()-time is so that some of the
// unit tests can adjust string Version and see theat reflected in Structured.
func Structured() semver.Version {
	// Cache the result to avoid re-doing work.
	if structuredInput == Version {
		return structuredOutput
	}
	var structured semver.Version
	switch Version {
	case "(devel)":
		structured = semver.MustParse("0.0.0-devel")
	case "(unknown version)":
		structured = semver.MustParse("0.0.0-unknownversion")
	default:
		var err error
		structured, err = semver.ParseTolerant(Version)
		if err != nil {
			// init() should not have let this happen
			panic(fmt.Errorf("this binary's version is unparsable: %w", err))
		}
	}
	structuredInput = Version
	structuredOutput = structured
	return structuredOutput
}

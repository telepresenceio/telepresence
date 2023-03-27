package version

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/blang/semver"
)

// Version is a "vSEMVER" string, and is either populated at build-time using `--ldflags -X`, or at
// init()-time by inspecting the binary's own debug info.
var (
	Version    string         //nolint:gochecknoglobals // constant
	Structured semver.Version //nolint:gochecknoglobals // constant
)

func init() {
	// Prefer version number inserted at build using --ldflags, but if it's not set...
	Version, Structured = Init(Version, "TELEPRESENCE_VERSION")
}

func Init(s, envKey string) (string, semver.Version) {
	if s == "" {
		if info, ok := debug.ReadBuildInfo(); ok {
			// Fall back to version info from "go get"
			s = info.Main.Version
		}
		if s == "" {
			if s = os.Getenv(envKey); s == "" {
				s = "0.0.0-unknown"
			}
		}
	}
	if s == "(devel)" {
		s = "0.0.0-devel"
	}
	s = strings.TrimPrefix(s, "v")
	sv, err := semver.Parse(s)
	if err != nil {
		panic(err)
	}
	return "v" + s, sv
}

func GetExecutable() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return executable, nil
}

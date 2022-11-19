package client

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/blang/semver"

	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

var DisplayName = "Telepresence" //nolint:gochecknoglobals // extension point

// Version returns the version of this executable.
func Version() string {
	return version.Version
}

func Semver() semver.Version {
	return version.Structured
}

func Executable() (string, error) {
	return version.GetExecutable()
}

// GetInstallMechanism returns how the executable was installed on the machine.
func GetInstallMechanism() (string, error) {
	execPath, err := os.Executable()
	mechanism := "undetermined"
	if err != nil {
		wrapErr := fmt.Errorf("unable to get exec path: %w", err)
		return mechanism, wrapErr
	}

	return GetMechanismFromPath(execPath)
}

// GetMechanismFromPath is a helper function that contains most of the logic
// required for GetInstallMechanism, but enables us to test it since we can
// control the path passed in.
func GetMechanismFromPath(execPath string) (string, error) {
	// Some package managers, like brew, symlink binaries into /usr/local/bin.
	// We want to use the actual location of the executable when reporting metrics
	// so we follow the symlink to get the actual binary path
	mechanism := "undetermined"
	binaryPath, err := filepath.EvalSymlinks(execPath)
	if err != nil {
		wrapErr := fmt.Errorf("error following executable symlink %s: %w", execPath, err)
		return mechanism, wrapErr
	}
	switch {
	case runtime.GOOS == "darwin" && strings.Contains(binaryPath, "Cellar"):
		mechanism = "brew"
	case strings.Contains(binaryPath, "docker"):
		mechanism = "docker"
	default:
		mechanism = "website"
	}
	return mechanism, nil
}

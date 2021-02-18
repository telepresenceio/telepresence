package filelocation

import (
	"context"
	"os"
	"path/filepath"
)

// SystemConfigDirs returns a list of directories to look for configuration files in.  The list is
// ordered in highest-precedence to lowest-precedence.  Every one of these directories is however
// lower precedence than UserConfigdir.
//
// This returns the value of the $XDG_CONFIG_DIRS environment variable if non-empty, or else
// "/etc/xdg".
//
// https://specifications.freedesktop.org/basedir-spec/basedir-spec-latest.html
//
// FIXME(lukeshu): Consider having SystemConfigDirs do something different on darwin/windows/plan9.
//
// FIXME(lukeshu): Consider having SystemConfigDirs vary depending on the installation prefix.
func systemConfigDirs(_ context.Context) ([]string, error) {
	str := os.Getenv("XDG_CONFIG_DIRS")
	if str == "" {
		str = "/etc/xdg"
	}
	return filepath.SplitList(str), nil
}

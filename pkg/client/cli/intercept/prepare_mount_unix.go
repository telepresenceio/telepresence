//go:build !windows
// +build !windows

package intercept

import (
	"os"
	"path/filepath"
)

func PrepareMount(cwd string, mountPoint string) (string, error) {
	if mountPoint == "" {
		return os.MkdirTemp("", "telfs-")
	}

	// filepath.Abs uses os.Getwd but we need the working dir of the cli
	if !filepath.IsAbs(mountPoint) {
		mountPoint = filepath.Join(cwd, mountPoint)
		mountPoint = filepath.Clean(mountPoint)
	}

	return mountPoint, os.MkdirAll(mountPoint, 0o700)
}

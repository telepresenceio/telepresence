//go:build !windows

package mount

import (
	"context"
	"os"
	"path/filepath"
)

func prepare(_ context.Context, cwd string, mountPoint string) (string, error) {
	if mountPoint == "" {
		// The user daemon creates the directory and reports it back as
		// ClientMountPoint.
		return "true", nil
	}

	// filepath.Abs uses os.Getwd but we need the working dir of the cli
	if !filepath.IsAbs(mountPoint) {
		mountPoint = filepath.Join(cwd, mountPoint)
		mountPoint = filepath.Clean(mountPoint)
	}

	return mountPoint, os.MkdirAll(mountPoint, 0o700)
}

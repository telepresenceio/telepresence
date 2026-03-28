//go:build !windows && !darwin

package mount

import (
	"context"
	"os"
	"path/filepath"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func prepare(ctx context.Context, cwd string, mountPoint string) (string, error) {
	if mountPoint == "" {
		return os.MkdirTemp(client.GetConfig(ctx).Intercept().MountsRoot, "telfs-")
	}

	// filepath.Abs uses os.Getwd but we need the working dir of the cli
	if !filepath.IsAbs(mountPoint) {
		mountPoint = filepath.Join(cwd, mountPoint)
		mountPoint = filepath.Clean(mountPoint)
	}

	return mountPoint, os.MkdirAll(mountPoint, 0o700)
}

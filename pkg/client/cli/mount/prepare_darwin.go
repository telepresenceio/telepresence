//go:build darwin

package mount

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func prepare(ctx context.Context, cwd string, mountPoint string) (string, error) {
	cfg := client.GetConfig(ctx).Intercept()
	if !cfg.UseMacosFsKit {
		return prepareDefault(ctx, cwd, mountPoint)
	}

	if mountPoint == "" {
		root := cfg.MountsRoot
		if root == "" {
			root = "/Volumes"
		}
		return os.MkdirTemp(root, "telfs-")
	}

	// filepath.Abs uses os.Getwd but we need the working dir of the cli
	if !filepath.IsAbs(mountPoint) {
		mountPoint = filepath.Join(cwd, mountPoint)
		mountPoint = filepath.Clean(mountPoint)
	}

	if !strings.HasPrefix(mountPoint, "/Volumes/") {
		return "", fmt.Errorf(
			"the FSKit backend requires mount points under /Volumes, but %q was specified; "+
				"either use a path under /Volumes or disable useMacosFsKit in your Telepresence config", mountPoint)
	}

	return mountPoint, os.MkdirAll(mountPoint, 0o700)
}

// prepareDefault is the standard unix behavior, used when FSKit is not active.
func prepareDefault(ctx context.Context, cwd string, mountPoint string) (string, error) {
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

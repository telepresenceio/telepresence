//go:build !embed_fuseftp && !docker

package remotefs

import (
	"context"
	"os/exec" //nolint:depguard // No use for dexec here
)

func getFuseFTPServer(_ context.Context, exe string) (string, error) {
	return exec.LookPath(exe)
}

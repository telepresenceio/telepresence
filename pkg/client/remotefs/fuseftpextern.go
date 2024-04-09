//go:build !embed_fuseftp && !docker

package remotefs

import (
	"context"
	_ "embed"
	"os/exec"
)

func getFuseFTPServer(_ context.Context, exe string) (string, error) {
	return exec.LookPath(exe)
}

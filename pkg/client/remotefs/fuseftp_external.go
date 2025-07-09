//go:build external_fuseftp && !docker

package remotefs

import (
	"context"
	"os/exec"
)

func getFuseFTPServer(_ context.Context, exe string) (string, error) {
	return exec.LookPath(exe)
}

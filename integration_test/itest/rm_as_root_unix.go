//go:build !windows
// +build !windows

package itest

//nolint:depguard // don't care about output or contexts
import (
	"context"
	"os/exec"

	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

func rmAsRoot(ctx context.Context, file string) error {
	// We just wanna make sure that the credentials are cached in a uniform way.
	if err := proc.CacheAdmin(ctx, ""); err != nil {
		return err
	}
	return exec.Command("sudo", "rm", "-f", file).Run()
}

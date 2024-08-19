//go:build !windows
// +build !windows

package itest

//nolint:depguard // don't care about output or contexts
import (
	"context"
	"os/exec"
)

func rmAsRoot(ctx context.Context, file string) error {
	return exec.Command("sudo", "-n", "rm", "-f", file).Run()
}

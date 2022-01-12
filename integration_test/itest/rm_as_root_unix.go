//go:build !windows
// +build !windows

package itest

//nolint:depguard // don't care about output or contexts
import "os/exec"

func rmAsRoot(file string) error {
	return exec.Command("sudo", "rm", "-f", file).Run()
}

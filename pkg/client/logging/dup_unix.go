//go:build !windows
// +build !windows

package logging

import (
	"os"

	"golang.org/x/sys/unix"
)

// dupToStdOut ensures that anything written to stdout will end up in the given file.
func dupToStdOut(file *os.File) error {
	// https://github.com/golang/go/issues/325
	if err := unix.Dup2(int(file.Fd()), 1); err != nil {
		return err
	}
	os.Stdout = file
	return nil
}

// dupToStdErr ensures that anything written to stderr will end up in the given file.
func dupToStdErr(file *os.File) error {
	// https://github.com/golang/go/issues/325
	if err := unix.Dup2(int(file.Fd()), 2); err != nil {
		return err
	}
	os.Stderr = file
	return nil
}

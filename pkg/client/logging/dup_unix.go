//go:build !windows
// +build !windows

package logging

import (
	"os"
	"syscall"
)

// dupToStd ensures that anything written to the file descriptor used by
// internal functions such as panic and println will end up in the given file.
func dupToStd(file *os.File) (err error) {
	// https://github.com/golang/go/issues/325

	fd := file.Fd()

	if err := syscall.Dup2(int(fd), 1); err != nil {
		return err
	}
	if err := syscall.Dup2(int(fd), 2); err != nil {
		return err
	}
	return nil
}

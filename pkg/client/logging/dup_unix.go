// +build linux darwin

package logging

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// dupToStd ensures that anything written to the file descriptor used by
// internal functions such as panic and println will end up in the given file.
//
// https://github.com/golang/go/issues/325
func dupToStd(file *os.File) (err error) {
	fd := file.Fd()

	// Dup2 to file descriptors 1 and 2 explicitly instead of using os.Stdout.Fd() and os.Stderr.Fd() since even if
	// the latter two may have been overridden, the builtin functions will still use 1 and 2.
	if err = syscall.Dup2(int(fd), 1); err == nil {
		err = syscall.Dup2(int(fd), 2)
	}
	return err
}

func dupStd() (int, int, error) {
	stdoutFd, err := unix.Dup(1)
	if err != nil {
		return -1, -1, err
	}
	stderrFd, err := unix.Dup(2)
	if err != nil {
		return -1, -1, err
	}
	return stdoutFd, stderrFd, err
}

func restoreStd(stdoutFd, stderrFd int) error {
	err := unix.Dup2(stdoutFd, 1)
	if err != nil {
		return err
	}
	err = unix.Dup2(stderrFd, 2)
	if err != nil {
		return err
	}
	return nil

}

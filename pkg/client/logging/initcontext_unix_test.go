//go:build !windows
// +build !windows

package logging

import (
	"golang.org/x/sys/unix"
)

func dupStd() (func(), error) {
	stdoutFd, err := unix.Dup(1)
	if err != nil {
		return nil, err
	}
	stderrFd, err := unix.Dup(2)
	if err != nil {
		return nil, err
	}
	return func() {
		_ = unix.Dup2(stdoutFd, 1)
		_ = unix.Dup2(stderrFd, 2)
	}, nil
}

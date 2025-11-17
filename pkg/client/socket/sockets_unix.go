//go:build !windows

package socket

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"

	"golang.org/x/sys/unix"

	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// userDaemonPath is the path used when communicating to the user daemon process.
func userDaemonPath(ctx context.Context) string {
	return "/tmp/telepresence-connector.socket"
}

// rootDaemonPath is the path used when communicating to the root daemon process.
func rootDaemonPath(ctx context.Context) string {
	return "/var/run/telepresence-daemon.socket"
}

func listen(_ context.Context, processName, socketName string) (net.Listener, error) {
	if proc.IsAdmin() {
		origUmask := unix.Umask(0)
		defer unix.Umask(origUmask)
	}
	listener, err := net.Listen("unix", socketName)
	if err != nil {
		if errors.Is(err, unix.EADDRINUSE) {
			err = fs.ErrExist
		}
		return nil, err
	}
	return listener, nil
}

// exists returns true if a socket is found at the given path.
func exists(path string) (bool, error) {
	s, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
		return false, err
	}
	if s.Mode()&os.ModeSocket == 0 {
		return false, fmt.Errorf("%q is not a socket", path)
	}
	return true, nil
}

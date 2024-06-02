//go:build !windows

package socket

import (
	"context"
	"errors"
	"fmt"
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
			err = fmt.Errorf("socket %q exists so the %s is either already running or terminated ungracefully", socketName, processName)
		}
		return nil, err
	}
	// Don't have dhttp.ServerConfig.Serve unlink the socket; defer unlinking the socket
	// until the process exits.
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	return listener, nil
}

// exists returns true if a socket is found at the given path.
func exists(path string) (bool, error) {
	s, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			err = nil
		}
		return false, err
	}
	if s.Mode()&os.ModeSocket == 0 {
		return false, fmt.Errorf("%q is not a socket", path)
	}
	return true, nil
}

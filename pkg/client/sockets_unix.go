//go:build !windows
// +build !windows

package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const (
	// ConnectorSocketName is the path used when communicating to the connector process
	ConnectorSocketName = "/tmp/telepresence-connector.socket"

	// DaemonSocketName is the path used when communicating to the daemon process
	DaemonSocketName = "/var/run/telepresence-daemon.socket"
)

func dialSocket(ctx context.Context, socketName string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second) // FIXME(lukeshu): Make this configurable
	defer cancel()
	for firstTry := true; ; firstTry = false {
		conn, err := grpc.DialContext(ctx, "unix:"+socketName, append([]grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithNoProxy(),
			grpc.WithBlock(),
			grpc.FailOnNonTempDialError(true),
		}, opts...)...)
		if err == nil {
			return conn, nil
		}

		// The google.golang.org/grpc/internal/transport.ConnectionError does not have an
		// Unwrap method. It does have a Origin method though.
		// See: https://github.com/grpc/grpc-go/pull/5148
		if oe, ok := err.(interface{ Origin() error }); ok {
			err = oe.Origin()
		}
		if firstTry && errors.Is(err, unix.ECONNREFUSED) {
			// Socket exists but doesn't accept connections. This usually means that the process
			// terminated ungracefully. To remedy this, we make an attempt to remove the socket
			// and dial again.
			if rmErr := os.Remove(socketName); rmErr != nil {
				err = fmt.Errorf("%w (socket rm failed with %v)", err, rmErr)
			} else {
				continue
			}
		}

		if err == context.DeadlineExceeded {
			// grpc.DialContext doesn't wrap context.DeadlineExceeded with any useful
			// information at all.  Fix that.
			err = &net.OpError{
				Op:  "dial",
				Net: "unix",
				Addr: &net.UnixAddr{
					Name: socketName,
					Net:  "unix",
				},
				Err: fmt.Errorf("socket exists but is not responding: %w", err),
			}
		}
		// Add some Telepresence-specific commentary on what specific common errors mean.
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			err = fmt.Errorf("%w; this usually means that the process has locked up", err)
		case errors.Is(err, unix.ECONNREFUSED):
			err = fmt.Errorf("%w; this usually means that the process has terminated ungracefully", err)
		case errors.Is(err, os.ErrNotExist):
			err = fmt.Errorf("%w; this usually means that the process is not running", err)
		}
		return nil, err
	}
}

func listenSocket(_ context.Context, processName, socketName string) (net.Listener, error) {
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

func removeSocket(listener net.Listener) error {
	return os.Remove(listener.Addr().String())
}

// socketExists returns true if a socket is found at the given path
func socketExists(path string) (bool, error) {
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

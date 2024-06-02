package socket

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
)

// UserDaemonPath is the path used when communicating to the user daemon process.
func UserDaemonPath(ctx context.Context) string {
	return userDaemonPath(ctx)
}

// RootDaemonPath is the path used when communicating to the root daemon process.
func RootDaemonPath(ctx context.Context) string {
	return rootDaemonPath(ctx)
}

func errNotExist(socketName string) error {
	return &net.OpError{
		Op:  "dial",
		Net: "unix",
		Addr: &net.UnixAddr{
			Name: socketName,
			Net:  "unix",
		},
		Err: fs.ErrNotExist,
	}
}

// Dial dials the given socket and returns the resulting connection.
func Dial(ctx context.Context, socketName string, waitForReady bool, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	if waitForReady {
		err := WaitForSocket(ctx, socketName, 5*time.Second)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				err = fmt.Errorf("%w; this usually means that the process is not running", errNotExist(socketName))
			}
			return nil, err
		}
	} else {
		ok, err := Exists(socketName)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errNotExist(socketName)
		}
	}

	conn, err := grpc.NewClient("unix:"+socketName, append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
	}, opts...)...)
	if err == nil && waitForReady {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = waitUntilReady(ctx, conn)
		cancel()
		if err != nil {
			// Socket exists but doesn't accept connections. This usually means that the process
			// terminated ungracefully. To remedy this, we make an attempt to remove the socket
			// and dial again.
			conn.Close()
			conn = nil
			if rmErr := os.Remove(socketName); rmErr != nil {
				err = fmt.Errorf("%w; remove of unresponsive socket failed: %v", err, rmErr)
			} else {
				err = fmt.Errorf("%w; socket unresponsive and removed", err)
			}
			err = fmt.Errorf("%w; this usually means that the process has locked up", &net.OpError{
				Op:  "dial",
				Net: "unix",
				Addr: &net.UnixAddr{
					Name: socketName,
					Net:  "unix",
				},
				Err: err,
			})
		}
	}
	if err != nil {
		err = fmt.Errorf("dial to socket %s failed: %w", socketName, err)
	}
	return conn, err
}

func waitUntilReady(ctx context.Context, cc *grpc.ClientConn) error {
	for {
		s := cc.GetState()
		switch s {
		case connectivity.Idle:
			cc.Connect()
		case connectivity.Ready:
			return nil
		case connectivity.Shutdown:
			return errors.New("connection closed")
		default:
		}
		if !cc.WaitForStateChange(ctx, s) {
			// ctx got timeout or canceled.
			return ctx.Err()
		}
	}
}

// Listen returns a listener for the given socket and returns the resulting connection.
func Listen(ctx context.Context, processName, socketName string) (net.Listener, error) {
	return listen(ctx, processName, socketName)
}

// Remove removes any representation of the socket from the filesystem.
func Remove(listener net.Listener) error {
	return os.Remove(listener.Addr().String())
}

// Exists returns true if a socket is found with the given name, false otherwise.
// An error is returned if the state of the socket cannot be determined, or if the
// found entry is not a socket.
func Exists(name string) (bool, error) {
	return exists(name)
}

// WaitUntilVanishes waits until the socket at the given path is removed
// and returns when that happens. The wait will be max ttw (time to wait) long.
// An error is returned if that time is exceeded before the socket is removed.
func WaitUntilVanishes(name, path string, ttw time.Duration) error {
	giveUp := time.Now().Add(ttw)
	for giveUp.After(time.Now()) {
		if exists, err := Exists(path); err != nil || !exists {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout while waiting for %s to exit", name)
}

// WaitForSocket waits until the socket at the given path comes into
// existence and returns when that happens. The wait will be max ttw (time to wait) long.
func WaitForSocket(ctx context.Context, path string, ttw time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, ttw)
	defer cancel()
	for ctx.Err() == nil {
		if ok, err := Exists(path); err != nil || ok {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("%w while waiting for socket %s", ctx.Err(), path)
}

// WaitUntilRunning waits until the socket at the given path comes into
// existence and a dial is successful and returns when that happens. The wait will
// be max ttw (time to wait) long.
func WaitUntilRunning(ctx context.Context, path string) error {
	conn, err := Dial(ctx, path, true)
	if err == nil {
		conn.Close()
	}
	return err
}

// IsRunning makes an attempt to dial the given socket and returns true if that
// succeeds. If the attempt doesn't succeed, the method returns false. No error is
// returned when the failed attempt is caused by a non-existing socket.
func IsRunning(ctx context.Context, path string) (bool, error) {
	conn, err := Dial(ctx, path, false)
	switch {
	case err == nil:
		err = waitUntilReady(ctx, conn)
		conn.Close()
		return true, err
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

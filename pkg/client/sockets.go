package client

import (
	"context"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
)

// DialSocket dials the given socket and returns the resulting connection
func DialSocket(ctx context.Context, socketName string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	return dialSocket(ctx, socketName, opts...)
}

// ListenSocket returns a listener for the given socket and returns the resulting connection
func ListenSocket(ctx context.Context, processName, socketName string) (net.Listener, error) {
	return listenSocket(ctx, processName, socketName)
}

// RemoveSocket removes any representation of the the socket from the filesystem.
func RemoveSocket(listener net.Listener) error {
	return removeSocket(listener)
}

// SocketExists returns true if a socket is found with the given name
func SocketExists(name string) bool {
	return socketExists(name)
}

// WaitUntilSocketVanishes waits until the socket at the given path is removed
// and returns when that happens. The wait will be max ttw (time to wait) long.
// An error is returned if that time is exceeded before the socket is removed.
func WaitUntilSocketVanishes(name, path string, ttw time.Duration) (err error) {
	giveUp := time.Now().Add(ttw)
	for giveUp.After(time.Now()) {
		if !SocketExists(path) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout while waiting for %s to exit", name)
}

// WaitUntilSocketAppears waits until the socket at the given path comes into
// existence and returns when that happens. The wait will be max ttw (time to wait) long.
// An error is returned if that time is exceeded before the socket is removed.
func WaitUntilSocketAppears(name, path string, ttw time.Duration) (err error) {
	giveUp := time.Now().Add(ttw)
	for giveUp.After(time.Now()) {
		if SocketExists(path) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout while waiting for %s to start", name)
}

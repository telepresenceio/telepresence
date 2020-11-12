package client

import (
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
)

const (
	// ConnectorSocketName is the path used when communicating to the connector process
	ConnectorSocketName = "/tmp/telepresence-connector.socket"

	// DaemonSocketName is the path used when communicating to the daemon process
	DaemonSocketName = "/var/run/telepresence-daemon.socket"
)

// SocketExists returns true if a socket is found at the given path
func SocketExists(path string) bool {
	s, err := os.Stat(path)
	return err == nil && s.Mode()&os.ModeSocket != 0
}

// WaitUntilSocketVanishes waits until the socket at the given path is removed
// and returns when that happens. The wait will be max ttw (time to wait) long.
// An error is returned if that time is exceeded before the socket is removed.
func WaitUntilSocketVanishes(name, path string, ttw time.Duration) (err error) {
	giveUp := time.Now().Add(ttw)
	for giveUp.After(time.Now()) {
		_, err = os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				err = nil
			}
			return err
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
		_, err = os.Stat(path)
		if err == nil {
			return
		}
		if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout while waiting for %s to exit", name)
}

// SocketURL returns the URL that corresponds to the given unix socket filesystem path.
func SocketURL(socket string) string {
	// TODO: Revise use of passthrough once this is fixed in grpc-go.
	//  see: https://github.com/grpc/grpc-go/issues/1741
	//  and https://github.com/grpc/grpc-go/issues/1911
	return "passthrough:///unix://" + socket
}

// DialSocket dials the given unix socket and returns the resulting connection
func DialSocket(socketName string) (*grpc.ClientConn, error) {
	return grpc.Dial(SocketURL(socketName), grpc.WithInsecure(), grpc.WithNoProxy())
}

package socket

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// userDaemonPath is the path used when communicating to the user daemon process.
func userDaemonPath(ctx context.Context) string {
	return filepath.Join(filelocation.AppUserCacheDir(ctx), "userd.socket")
}

// rootDaemonPath is the path used when communicating to the root daemon process.
func rootDaemonPath(ctx context.Context) string {
	return filepath.Join(filelocation.AppUserCacheDir(ctx), "rootd.socket")
}

func dial(ctx context.Context, socketName string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second) // FIXME(lukeshu): Make this configurable
	defer cancel()
	for firstTry := true; ; firstTry = false {
		// Windows will give us a WSAECONNREFUSED if the socket does not exist. That's not
		// what we want.
		found, err := exists(socketName)
		if err != nil {
			return nil, err
		}
		if !found {
			err = &net.OpError{
				Op:  "dial",
				Net: "unix",
				Addr: &net.UnixAddr{
					Name: socketName,
					Net:  "unix",
				},
				Err: fs.ErrNotExist,
			}
		}
		if err == nil {
			conn, dialErr := grpc.DialContext(ctx, "unix:"+socketName, append([]grpc.DialOption{
				grpc.WithTransportCredentials(insecure.NewCredentials()),
				grpc.WithNoProxy(),
				grpc.WithBlock(),
				grpc.FailOnNonTempDialError(true),
			}, opts...)...)
			if dialErr == nil {
				return conn, nil
			}
			err = dialErr
		}

		// Remove the gRPC internal transport.Connection error wrapper. It messes up the message by
		// quoting it so that backslashes in the path get doubled.
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			err = opErr
		}

		// Windows will give us a WSAECONNREFUSED if the socket does not exist. That's not
		// what we want.
		if errors.Is(err, windows.WSAECONNREFUSED) {
			found, exErr := exists(socketName)
			if exErr != nil {
				return nil, exErr
			}
			if !found {
				err = &net.OpError{
					Op:  "dial",
					Net: "unix",
					Addr: &net.UnixAddr{
						Name: socketName,
						Net:  "unix",
					},
					Err: fs.ErrNotExist,
				}
			}
		}

		if firstTry && errors.Is(err, windows.WSAECONNREFUSED) {
			// Socket exists but doesn't accept connections. This usually means that the process
			// terminated ungracefully. To remedy this, we make an attempt to remove the socket
			// and dial again.
			dlog.Errorf(ctx, "Dial unix:%s failed: %v", socketName, err)
			if rmErr := os.Remove(socketName); rmErr != nil && !errors.Is(err, os.ErrNotExist) {
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
		case errors.Is(err, windows.WSAECONNREFUSED):
			err = fmt.Errorf("%w; this usually means that the process has terminated ungracefully", err)
		case errors.Is(err, os.ErrNotExist):
			err = fmt.Errorf("%w; this usually means that the process is not running", err)
		}
		return nil, err
	}
}

// listen returns a listener for the given socket and returns the resulting connection.
func listen(ctx context.Context, processName, socketName string) (net.Listener, error) {
	listener, err := net.Listen("unix", socketName)
	if err != nil {
		if err != nil {
			err = fmt.Errorf("socket %q exists so the %s is either already running or terminated ungracefully: %T, %w", socketName, processName, err, err)
		}
		return nil, err
	}
	// Don't have dhttp.ServerConfig.Serve unlink the socket; defer unlinking the socket
	// until the process exits.
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	return listener, nil
}

// socketAttributes is the combination that Windows uses for Unix socket FileAttributes.
const socketAttributes = windows.FILE_ATTRIBUTE_REPARSE_POINT | windows.FILE_ATTRIBUTE_ARCHIVE

// exists returns true if a socket is found at the given path.
func exists(path string) (bool, error) {
	namep, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}

	var fa windows.Win32FileAttributeData
	err = windows.GetFileAttributesEx(namep, windows.GetFileExInfoStandard, (*byte)(unsafe.Pointer(&fa)))
	if err != nil {
		return false, nil
	}
	if fa.FileAttributes&socketAttributes != socketAttributes {
		return false, fmt.Errorf("%q is not a socket", path)
	}
	return true, nil
}

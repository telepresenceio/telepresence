package client

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// The Windows IPC between the CLI and the user and root daemons is based on named pipes rather than
// unix sockets.
// See https://docs.microsoft.com/en-us/windows/win32/ipc/pipe-names for more info
// about pipe names.
const (
	// ConnectorSocketName is the name used when communicating to the connector process
	ConnectorSocketName = `\\.\pipe\telepresence-connector`

	// DaemonSocketName is the name used when communicating to the daemon process
	DaemonSocketName = `\\.\pipe\telepresence-daemon`
)

// dialSocket dials the given named pipe and returns the resulting connection
func dialSocket(c context.Context, socketName string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
	conn, err := grpc.DialContext(c, socketName, append([]grpc.DialOption{
		grpc.WithInsecure(),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.FailOnNonTempDialError(true),
		grpc.WithContextDialer(func(c context.Context, s string) (net.Conn, error) {
			conn, err := winio.DialPipeContext(c, socketName)
			return conn, err
		}),
	}, opts...)...)
	return conn, err
}

// allowEveryone is a security descriptor that allows everyone to perform the an action.
// For more info about the syntax, sse:
// https://docs.microsoft.com/en-us/windows/win32/secauthz/security-descriptor-string-format
const allowEveryone = "S:(ML;;NW;;;LW)D:(A;;0x12019f;;;WD)"

// listenSocket returns a listener for the given named pipe and returns the resulting connection
func listenSocket(_ context.Context, processName, socketName string) (net.Listener, error) {
	var config *winio.PipeConfig
	if proc.IsAdmin() {
		config = &winio.PipeConfig{SecurityDescriptor: allowEveryone}
	}
	return winio.ListenPipe(socketName, config)
}

// removeSocket does nothing because a named pipe has no representation in the file system that
// needs to be removed
func removeSocket(listener net.Listener) error {
	return nil
}

// socketExists returns true if a socket exists with the given name
func socketExists(name string) bool {
	uPath, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false
	}

	// Despite the name of the function, this is actually an attempt to open an existing socket. The
	// OPEN_EXISTING disposition will make it fail unless it exists.
	h, err := windows.CreateFile(uPath, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		// ERROR_PIPE_BUSY is an error that is issued somewhat sporadically but it's a safe
		// indication that the pipe exists.
		return err == windows.ERROR_PIPE_BUSY
	}
	ft, err := windows.GetFileType(h)
	_ = windows.CloseHandle(h)
	return err == nil && ft|windows.FILE_TYPE_PIPE != 0
}

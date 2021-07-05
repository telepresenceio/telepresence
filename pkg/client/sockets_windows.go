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
const (
	// ConnectorSocketName is the path used when communicating to the connector process
	ConnectorSocketName = `\\.\pipe\telepresence-connector`

	// DaemonSocketName is the path used when communicating to the daemon process
	DaemonSocketName = `\\.\pipe\telepresence-daemon`
)

// DialSocket dials the given named pipet and returns the resulting connection
func DialSocket(c context.Context, socketName string) (*grpc.ClientConn, error) {
	conn, err := grpc.DialContext(c, socketName,
		grpc.WithInsecure(),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.FailOnNonTempDialError(true),
		grpc.WithContextDialer(func(c context.Context, s string) (net.Conn, error) {
			conn, err := winio.DialPipeContext(c, socketName)
			return conn, err
		}))
	return conn, err
}

const AllowEveryone = "S:(ML;;NW;;;LW)D:(A;;0x12019f;;;WD)"

// ListenSocket returns a listener for the given named pipe and returns the resulting connection
func ListenSocket(_ context.Context, processName, socketName string) (net.Listener, error) {
	var config *winio.PipeConfig
	if proc.IsAdmin() {
		config = &winio.PipeConfig{SecurityDescriptor: AllowEveryone}
	}
	return winio.ListenPipe(socketName, config)
}

// SocketExists returns true if a socket is found at the given path
func SocketExists(path string) bool {
	uPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(uPath, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		// ERROR_PIPE_BUSY is an error that is issued somewhat sporadically but it's a safe
		// indication that the pipe exists.
		return err == windows.ERROR_PIPE_BUSY
	}
	defer windows.CloseHandle(h)
	ft, err := windows.GetFileType(h)
	return err == nil && ft|windows.FILE_TYPE_PIPE != 0
}

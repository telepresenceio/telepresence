package socket

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"

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

// listen returns a listener for the given socket and returns the resulting connection.
func listen(ctx context.Context, processName, socketName string) (net.Listener, error) {
	listener, err := net.Listen("unix", socketName)
	if err != nil {
		err = fmt.Errorf("socket %q exists so the %s is either already running or terminated ungracefully: %T, %w", socketName, processName, err, err)
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

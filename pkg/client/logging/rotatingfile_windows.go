package logging

import (
	"os"

	"golang.org/x/sys/windows"
)

// createFile creates a new file or truncates an existing file. The
// file is opened in a way that makes it possible to rename it without
// first closing it.
func createFile(path string, _ os.FileMode) (*os.File, error) {
	uPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// The FILE_SHARE_xxx flags are needed to make it possible to rename a file
	// while its still in use by a process.
	h, err := windows.CreateFile(uPath,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.CREATE_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// openForAppend opens a file for append or creates it if it doesn't exist. The
// file is opened in a way that makes it possible to rename it without first closing it.
func openForAppend(path string, _ os.FileMode) (*os.File, error) {
	uPath, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// The FILE_SHARE_xxx flags are needed to make it possible to rename a file
	// while its still in use by a process.
	h, err := windows.CreateFile(uPath,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_ALWAYS,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_WRITE_THROUGH, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

// IsTerminal returns whether the given file descriptor is a terminal
var IsTerminal = func(fd int) bool {
	return false
}

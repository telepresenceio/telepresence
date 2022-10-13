package logging

import (
	"time"

	"golang.org/x/sys/windows"
)

// IsTerminal returns whether the given file descriptor is a terminal.
var IsTerminal = func(fd int) bool { //nolint:gochecknoglobals // os specific func replacement
	return false
}

// restoreCTimeAfterRename will restore the creation time on a file on Windows where
// the file otherwise gets the creation time of the existing file that the operation
// overwrites.
func restoreCTimeAfterRename(path string, ctime time.Time) error {
	p16, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return e
	}
	h, e := windows.CreateFile(p16,
		windows.FILE_WRITE_ATTRIBUTES, windows.FILE_SHARE_WRITE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if e != nil {
		return e
	}
	defer windows.Close(h)
	c := windows.NsecToFiletime(ctime.UnixNano())
	return windows.SetFileTime(h, &c, nil, nil)
}

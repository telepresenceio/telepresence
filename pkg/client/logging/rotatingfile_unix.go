//go:build !windows

package logging

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
)

// noFollow makes opening a path fail when its final component is a symlink.
const noFollow = unix.O_NOFOLLOW

// chownFile changes the owner of the open file, never of whatever its path currently names.
func chownFile(f dos.File, uid, gid int) error {
	of, ok := f.(*os.File)
	if !ok {
		return fmt.Errorf("chown is not supported by %T", f)
	}
	return of.Chown(uid, gid)
}

// restoreCTimeAfterRename is a noop on unixes since the renamed file retains the creation time of the source.
func restoreCTimeAfterRename(_ string, _ time.Time) error {
	return nil
}

// IsTerminal returns whether the given file descriptor is a terminal.
var IsTerminal = term.IsTerminal //nolint:gochecknoglobals // os specific func replacement

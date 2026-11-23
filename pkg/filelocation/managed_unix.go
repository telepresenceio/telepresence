//go:build !windows

package filelocation

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall" //nolint:depguard // We specifically need "syscall.Stat_t" for fs.FileInfo.Sys().
)

func checkManaged(path, root string) error {
	return checkManagedOwner(path, root, 0)
}

// checkManagedOwner walks from path up to root. Each element must be owned by the given user, not be writable by
// group or others, and not be a symbolic link.
func checkManagedOwner(path, root string, owner uint32) error {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return &UntrustedError{Path: path, Reason: fmt.Sprintf("it is not located under %s", root)}
	}
	for p := path; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return &UntrustedError{Path: path, Reason: fmt.Sprintf("%s is a symbolic link", p)}
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			return &UntrustedError{Path: path, Reason: fmt.Sprintf("the owner of %s cannot be determined", p)}
		}
		if st.Uid != owner {
			return &UntrustedError{Path: path, Reason: fmt.Sprintf("%s is owned by user %d, not by %d", p, st.Uid, owner)}
		}
		if fi.Mode().Perm()&0o022 != 0 {
			return &UntrustedError{Path: path, Reason: fmt.Sprintf("%s is writable by group or others (mode %04o)", p, fi.Mode().Perm())}
		}
		if p == root {
			return nil
		}
	}
}

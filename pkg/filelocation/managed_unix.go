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
// group or others, not be a symbolic link, and (on macOS) have no access control entry that allows changes.
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
		acl, err := aclWriter(p)
		if err != nil {
			return &UntrustedError{Path: path, Reason: err.Error()}
		}
		if acl != "" {
			return &UntrustedError{Path: path, Reason: fmt.Sprintf("%s has an access control entry that allows changes: %s", p, acl)}
		}
		if p == root {
			return nil
		}
	}
}

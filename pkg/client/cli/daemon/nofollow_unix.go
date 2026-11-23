//go:build !windows

package daemon

import "golang.org/x/sys/unix"

// noFollow makes opening a path fail when its final component is a symlink.
const noFollow = unix.O_NOFOLLOW

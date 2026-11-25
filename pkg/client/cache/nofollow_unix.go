//go:build !windows

package cache

import "golang.org/x/sys/unix"

// noFollow makes opening a path fail when its final component is a symlink.
const noFollow = unix.O_NOFOLLOW

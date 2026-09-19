//go:build !windows

package setup

import "golang.org/x/sys/unix"

// errConnRefused is the socket error a host returns when nothing listens on the dialed port.
const errConnRefused = unix.ECONNREFUSED

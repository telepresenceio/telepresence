//go:build linux

package quicforwarder

import (
	"errors"

	"golang.org/x/sys/unix"
)

// readable reports whether the socket fd has a datagram queued, or an error pending, without
// consuming it.
func readable(fd uintptr) bool {
	var b [1]byte
	_, _, err := unix.Recvfrom(int(fd), b[:], unix.MSG_PEEK|unix.MSG_DONTWAIT)
	return !errors.Is(err, unix.EAGAIN)
}

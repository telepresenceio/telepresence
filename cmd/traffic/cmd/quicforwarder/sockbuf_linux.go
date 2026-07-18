//go:build linux

package quicforwarder

import (
	"net"

	"golang.org/x/sys/unix"
)

// socketBufferSizes reads back SO_RCVBUF/SO_SNDBUF. The kernel reports twice the
// usable value (it accounts for its own overhead in the same counter); the raw
// numbers are reported as-is since they are only logged for diagnostics.
func socketBufferSizes(conn *net.UDPConn) (rcv, snd int) {
	rc, err := conn.SyscallConn()
	if err != nil {
		return 0, 0
	}
	_ = rc.Control(func(fd uintptr) {
		rcv, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF)
		snd, _ = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF)
	})
	return rcv, snd
}

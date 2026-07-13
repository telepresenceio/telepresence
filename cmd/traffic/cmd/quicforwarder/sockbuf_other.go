//go:build !linux

package quicforwarder

import "net"

func socketBufferSizes(*net.UDPConn) (rcv, snd int) {
	return 0, 0
}

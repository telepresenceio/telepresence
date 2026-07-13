package quicforwarder

import "net"

// desiredSocketBuffer is the SO_RCVBUF/SO_SNDBUF size each of the forwarder's UDP
// sockets asks for. A relay that drops a datagram because its socket buffer
// overflowed converts a scheduling hiccup into QUIC packet loss, and every such loss
// slashes the sender's congestion window -- so the buffers must absorb at least a
// slow-start microburst (a paced sender emits bursts of several hundred KB of GSO
// batches). The kernel silently caps the grant at net.core.{r,w}mem_max; see
// raiseSocketBuffers for how the achieved size is made visible.
const desiredSocketBuffer = 16 << 20

// raiseSocketBuffers asks for desiredSocketBuffer in both directions on conn and
// reports the sizes the kernel actually granted (0 on platforms where they cannot be
// read back). Callers log the result: a grant far below desiredSocketBuffer means the
// node's net.core.rmem_max/wmem_max cap the relay's burst absorption and with it the
// achievable QUIC throughput.
func raiseSocketBuffers(conn *net.UDPConn) (rcv, snd int) {
	_ = conn.SetReadBuffer(desiredSocketBuffer)
	_ = conn.SetWriteBuffer(desiredSocketBuffer)
	return socketBufferSizes(conn)
}

package quicforwarder

import (
	"golang.org/x/net/ipv4"
)

// batchSize bounds how many datagrams a single ReadBatch/WriteBatch call attempts to
// move in one syscall (recvmmsg/sendmmsg on Linux). On platforms where x/net has no
// batch syscall (darwin, windows, ...), ipv4.PacketConn.ReadBatch/WriteBatch fall back
// to handling exactly one message per call regardless of len(ms) -- see their doc
// comments -- so this package still compiles and runs correctly there, just without the
// batching speedup.
const batchSize = 64

// ipv4.NewPacketConn is used for every batch conn in this package, front and per-flow
// backend alike, regardless of the wrapped socket's address family: ipv4.Message and
// ipv6.Message are both plain aliases of golang.org/x/net/internal/socket.Message, and
// ReadBatch/WriteBatch (recvmmsg/sendmmsg) don't interpret address families at all --
// they hand back/take whatever net.Addr the OS's sockaddr resolves to. quic-go's own
// oobConn does exactly this (always ipv4.NewPacketConn, see sys_conn_oob.go), including
// for its own dual-stack wildcard-bound sockets. Verified directly against this
// package's front socket (net.ListenUDP("udp", ...), which binds an AF_INET6 socket
// with IPV6_V6ONLY off): both a v4 and a v6 client's datagrams arrive through one
// ipv4.PacketConn's ReadBatch, addressed as *net.UDPAddr (v4 clients arrive as
// IPv4-in-IPv6-mapped netip.Addrs, exactly as net.UDPConn.ReadFromUDPAddrPort already
// reported them before this change), and writing back to that mapped address through
// the same ipv4.PacketConn's WriteBatch reaches the v4 client correctly.

// newBatchMessages allocates n reusable maxDatagramSize-capacity messages for a
// ReadBatch loop, so repeated ReadBatch calls reuse the same backing arrays instead of
// allocating fresh buffers per datagram.
func newBatchMessages(n int) []ipv4.Message {
	msgs := make([]ipv4.Message, n)
	for i := range msgs {
		msgs[i].Buffers = [][]byte{make([]byte, maxDatagramSize)}
	}
	return msgs
}

// writeBatchAll writes every message in msgs, looping over PacketConn.WriteBatch (whose
// single call can return a short count with no error) until all of them are sent or a
// real error occurs.
func writeBatchAll(pc *ipv4.PacketConn, msgs []ipv4.Message) error {
	for len(msgs) > 0 {
		n, err := pc.WriteBatch(msgs, 0)
		if err != nil {
			return err
		}
		if n <= 0 {
			return nil
		}
		msgs = msgs[n:]
	}
	return nil
}

// msgsForDatagrams builds a []ipv4.Message wrapping each of datagrams for a single
// writeBatchAll call. Used only on the cold new-flow path (a handshake's buffered
// datagrams, capped at handshakeMaxDatagrams), so one small allocation here is fine.
func msgsForDatagrams(datagrams [][]byte) []ipv4.Message {
	msgs := make([]ipv4.Message, len(datagrams))
	for i, dg := range datagrams {
		msgs[i].Buffers = [][]byte{dg}
	}
	return msgs
}

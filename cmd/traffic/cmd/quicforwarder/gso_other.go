//go:build !linux

package quicforwarder

import (
	"net"

	"golang.org/x/net/ipv4"
)

// writeMsgsBatch writes msgs with plain sendmmsg batching: UDP_SEGMENT (generic
// segmentation offload) is a Linux-only kernel feature, so there is no GSO path to try
// here. writeBatchAll's own fallback (one message per call, per ipv4.PacketConn's
// documented non-Linux behavior) is exactly as correct, just without GSO's extra
// speedup.
func writeMsgsBatch(pc *ipv4.PacketConn, msgs []ipv4.Message, _ []byte) error {
	return writeBatchAll(pc, msgs)
}

// gsoSupported is always false: UDP_SEGMENT is a Linux-only kernel feature. Exists so
// forwarder.go's startup and periodic logging can report it uniformly across platforms.
func gsoSupported() bool { return false }

// enableGRO is a no-op: UDP_GRO (generic receive offload) is a Linux-only kernel
// feature, same as UDP_SEGMENT above.
func enableGRO(*net.UDPConn) bool { return false }

// newReadBatchMessages ignores gro (always false here, since enableGRO always is) and
// allocates plain messages; see newBatchMessages.
func newReadBatchMessages(n int, _ bool) []ipv4.Message {
	return newBatchMessages(n)
}

// splitGRO is the identity split: with GRO unsupported on this platform, a ReadBatch
// message is always exactly one wire datagram already.
func splitGRO(msg *ipv4.Message, yield func(data []byte)) {
	yield(msg.Buffers[0][:msg.N])
}

//go:build !linux

package quicforwarder

import (
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

//go:build linux

package quicforwarder

import (
	"encoding/binary"
	"net"
	"sync"
	"unsafe"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// gsoSupported reports whether this kernel accepts UDP_SEGMENT (UDP generic
// segmentation offload), probed once and cached: a plain sendmmsg/recvmmsg batch still
// pays the same per-datagram kernel-side cost (route lookup, checksum, skb allocation)
// as one sendmsg/recvmsg per datagram would -- batching only removes the syscall
// entry/exit overhead, which measurement on real traffic through this forwarder showed
// is a small fraction of that per-datagram cost. GSO removes the per-datagram cost
// itself: the kernel does routing/checksum/etc. once for a whole coalesced buffer and
// segments it into wire datagrams far more cheaply, provided every datagram but
// possibly the last is exactly the same size and shares one destination -- exactly the
// shape of a steady-state, single-flow QUIC data stream.
//
//nolint:gochecknoglobals // process-wide kernel capability, probed once and cached.
var gsoSupported = sync.OnceValue(func() bool {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return false
	}
	defer c.Close()
	rc, err := c.SyscallConn()
	if err != nil {
		return false
	}
	var setErr error
	if err := rc.Control(func(fd uintptr) {
		setErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_SEGMENT, maxDatagramSize)
	}); err != nil {
		return false
	}
	return setErr == nil
})

// gsoOOB returns the UDP_SEGMENT control message that tells the kernel to split one
// coalesced write into datagrams of segmentSize bytes each (the last one may be
// shorter).
func gsoOOB(segmentSize int) []byte {
	b := make([]byte, unix.CmsgSpace(2))
	hdr := (*unix.Cmsghdr)(unsafe.Pointer(&b[0]))
	hdr.Level = unix.IPPROTO_UDP
	hdr.Type = unix.UDP_SEGMENT
	hdr.SetLen(unix.CmsgLen(2))
	binary.NativeEndian.PutUint16(b[unix.CmsgLen(0):], uint16(segmentSize))
	return b
}

// writeMsgsBatch writes msgs -- one flow's consecutive datagrams to one destination --
// in arrival order, GSO-coalescing every maximal run of two or more uniformly sized
// datagrams (a run may end in one shorter datagram, matching UDP_SEGMENT's own "last
// segment may be shorter" contract) and folding every other stretch into as few plain
// sendmmsg batches as possible. A single ReadBatch's worth of a real QUIC stream is
// rarely one uniform run start-to-finish -- pacing gaps, ACK-only datagrams, and the
// final short datagram of a burst all break it up -- so eligibility is checked
// per-run, not once for the whole slice.
func writeMsgsBatch(pc *ipv4.PacketConn, msgs []ipv4.Message, scratch []byte) error {
	supported := gsoSupported()
	for len(msgs) > 0 {
		if supported {
			if runLen, segSize, ok := gsoRunAt(msgs, 0); ok {
				if err := writeGSOBatch(pc, msgs[:runLen], segSize, scratch); err != nil {
					return err
				}
				msgs = msgs[runLen:]
				continue
			}
		}
		// msgs[0] doesn't start a GSO-eligible run. Extend over every following
		// position that doesn't start one either, and send that whole irregular
		// stretch as one plain batch -- still one sendmmsg call, not one write()
		// per datagram.
		end := 1
		for end < len(msgs) {
			if supported {
				if _, _, ok := gsoRunAt(msgs, end); ok {
					break
				}
			}
			end++
		}
		if err := writeBatchAll(pc, msgs[:end]); err != nil {
			return err
		}
		msgs = msgs[end:]
	}
	return nil
}

// gsoRunAt reports whether msgs[at:] begins with a GSO-eligible run: two or more
// messages of the same size, optionally followed by one shorter message ending the
// run.
func gsoRunAt(msgs []ipv4.Message, at int) (runLen, segSize int, ok bool) {
	segSize = msgLen(msgs[at])
	if segSize == 0 || at+1 >= len(msgs) {
		return 0, 0, false
	}
	if msgLen(msgs[at+1]) != segSize {
		if l := msgLen(msgs[at+1]); l > 0 && l < segSize {
			return 2, segSize, true
		}
		return 0, 0, false
	}
	n := at + 2
	for n < len(msgs) && msgLen(msgs[n]) == segSize {
		n++
	}
	if n < len(msgs) {
		if l := msgLen(msgs[n]); l > 0 && l < segSize {
			n++
		}
	}
	return n - at, segSize, true
}

// writeGSOBatch sends msgs (already confirmed GSO-eligible for segSize by gsoRunAt) as
// one or more coalesced GSO writes, chunked to fit scratch (sized so
// segSize*maxPerCall stays within the kernel's own limit on one coalesced UDP write).
// It assembles each chunk into scratch (one memcpy per source datagram -- far cheaper
// than the per-datagram kernel-side route/checksum/skb work GSO itself avoids) and
// issues one WriteBatch per chunk. On error it stops and returns immediately: the
// eligibility check already ran before any I/O, so there is no partial-GSO/plain-fallback
// overlap to reconcile -- the caller just logs the error, exactly as it already does
// for writeBatchAll, and the rest of this chunk's-worth of datagrams is silently
// dropped, matching this package's existing drop-on-write-error behavior.
func writeGSOBatch(pc *ipv4.PacketConn, msgs []ipv4.Message, segSize int, scratch []byte) error {
	maxPerCall := max(len(scratch)/segSize, 1)
	oob := gsoOOB(segSize)
	for len(msgs) > 0 {
		n := min(len(msgs), maxPerCall)
		chunk := msgs[:n]
		off := 0
		for _, m := range chunk {
			for _, b := range m.Buffers {
				off += copy(scratch[off:], b)
			}
		}
		gm := ipv4.Message{Buffers: [][]byte{scratch[:off]}, OOB: oob, Addr: chunk[0].Addr}
		if _, err := pc.WriteBatch([]ipv4.Message{gm}, 0); err != nil {
			return err
		}
		msgs = msgs[n:]
	}
	return nil
}

func msgLen(m ipv4.Message) int {
	n := 0
	for _, b := range m.Buffers {
		n += len(b)
	}
	return n
}

//go:build linux

package quicforwarder

import (
	"encoding/binary"
	"net"
	"os"
	"strconv"
	"sync"
	"unsafe"

	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// gsoSupported reports whether this kernel accepts UDP_SEGMENT (UDP generic
// segmentation offload), probed once and cached: a plain sendmmsg/recvmmsg batch
// still pays the same per-datagram kernel-side cost (route lookup, checksum, skb
// allocation) as one sendmsg/recvmsg per datagram would -- batching only removes
// the syscall entry/exit overhead, a small fraction of that per-datagram cost. GSO
// removes the per-datagram cost itself: the kernel does routing/checksum/etc. once
// for a whole coalesced buffer and segments it into wire datagrams far more cheaply,
// provided every datagram but possibly the last is exactly the same size and shares
// one destination -- exactly the shape of a steady-state, single-flow QUIC data stream.
//
//nolint:gochecknoglobals // process-wide kernel capability, probed once and cached.
var gsoSupported = sync.OnceValue(func() bool {
	// Honor quic-go's own escape hatch so a single env var disables UDP GSO
	// uniformly across every process in the QUIC path (manager, agent, and this
	// forwarder). Needed when loss is injected with tc netem for testing: netem
	// sits above the segmentation step, so a GSO super-packet is dropped as one
	// unit of up to ~47 datagrams, wildly distorting the effective loss rate.
	if disabled, err := strconv.ParseBool(os.Getenv("QUIC_GO_DISABLE_GSO")); err == nil && disabled {
		return false
	}
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

// groSupported reports whether this kernel accepts UDP_GRO (generic receive offload),
// probed once and cached, mirroring gsoSupported: the read-side counterpart of GSO
// above, it lets the kernel coalesce a run of same-size datagrams from one peer into a
// single ReadBatch message plus a control message carrying the original segment size,
// cutting the per-datagram receive cost (route lookup, checksum, skb allocation) at high
// datagram rates roughly in half.
//
//nolint:gochecknoglobals // process-wide kernel capability, probed once and cached.
var groSupported = sync.OnceValue(func() bool {
	// Same escape hatch as gsoSupported, for the same reason: netem-injected loss
	// sits above the coalescing step, so a GRO super-datagram would be dropped as one
	// unit, distorting the effective loss rate.
	if disabled, err := strconv.ParseBool(os.Getenv("QUIC_GO_DISABLE_GSO")); err == nil && disabled {
		return false
	}
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
		setErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_GRO, 1)
	}); err != nil {
		return false
	}
	return setErr == nil
})

// enableGRO turns on UDP_GRO for conn when the kernel supports it (see groSupported) and
// reports whether it did: callers need that to know whether to allocate a control-message
// buffer for reads on this socket (newReadBatchMessages) and whether splitGRO might ever
// have real coalescing to undo.
func enableGRO(conn *net.UDPConn) bool {
	if !groSupported() {
		return false
	}
	rc, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var setErr error
	if err := rc.Control(func(fd uintptr) {
		setErr = unix.SetsockoptInt(int(fd), unix.IPPROTO_UDP, unix.UDP_GRO, 1)
	}); err != nil {
		return false
	}
	return setErr == nil
}

// groCmsgSpace is the control-message buffer size a ReadBatch message needs to receive a
// UDP_GRO control message (one uint16 segment size).
var groCmsgSpace = unix.CmsgSpace(2) //nolint:gochecknoglobals // computed once, read-only.

// newReadBatchMessages allocates n reusable maxDatagramSize-capacity messages for a
// ReadBatch loop, as newBatchMessages does, additionally giving each one a control-message
// buffer when gro is true (see enableGRO) so a coalesced read's UDP_GRO segment size
// reaches splitGRO.
func newReadBatchMessages(n int, gro bool) []ipv4.Message {
	msgs := newBatchMessages(n)
	if gro {
		for i := range msgs {
			msgs[i].OOB = make([]byte, groCmsgSpace)
		}
	}
	return msgs
}

// splitGRO calls yield once per wire-sized datagram in a ReadBatch message msg, in
// arrival order. Normally that is a single call with msg's whole payload; with UDP_GRO
// enabled (enableGRO) the kernel may instead have coalesced a run of same-size datagrams
// from one peer into msg's single Buffers[0] plus a UDP_GRO control message carrying the
// original segment size, and every layer downstream of this call -- backend routing,
// allowlist lookups, QUIC packet parsing -- expects one UDP datagram per call, so this
// expands that back out before any of them see it. Every yielded slice aliases msg's own
// buffer and is only valid until the next ReadBatch call reuses it, exactly like msg's
// payload already was before GRO.
func splitGRO(msg *ipv4.Message, yield func(data []byte)) {
	data := msg.Buffers[0][:msg.N]
	segSize := groSegmentSize(msg.OOB[:msg.NN])
	if segSize <= 0 || segSize >= len(data) {
		yield(data)
		return
	}
	for len(data) > segSize {
		yield(data[:segSize])
		data = data[segSize:]
	}
	yield(data)
}

// groSegmentSize parses oob (a ReadBatch message's actually-filled control-message
// bytes, msg.OOB[:msg.NN]) for a UDP_GRO control message and returns the segment size it
// carries, or 0 if none is present -- GRO wasn't enabled for this socket, or the kernel
// chose not to coalesce this particular read (most reads, even with GRO enabled: it only
// fires for a genuine back-to-back run from the same peer).
func groSegmentSize(oob []byte) int {
	if len(oob) == 0 {
		return 0
	}
	cmsgs, err := unix.ParseSocketControlMessage(oob)
	if err != nil {
		return 0
	}
	for _, m := range cmsgs {
		if m.Header.Level == unix.IPPROTO_UDP && m.Header.Type == unix.UDP_GRO && len(m.Data) >= 2 {
			return int(binary.NativeEndian.Uint16(m.Data))
		}
	}
	return 0
}

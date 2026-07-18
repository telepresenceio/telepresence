//go:build linux

package quicforwarder

import (
	"encoding/binary"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

// groOOB builds a synthetic UDP_GRO control message carrying segSize, in the same wire
// shape the kernel would attach to a ReadBatch message on a socket with UDP_GRO enabled
// (see enableGRO). Mirrors gsoOOB's construction (same cmsg layout, IPPROTO_UDP level,
// one uint16 payload), just with the read-side type instead of the write-side one.
func groOOB(segSize int) []byte {
	b := make([]byte, unix.CmsgSpace(2))
	hdr := (*unix.Cmsghdr)(unsafe.Pointer(&b[0]))
	hdr.Level = unix.IPPROTO_UDP
	hdr.Type = unix.UDP_GRO
	hdr.SetLen(unix.CmsgLen(2))
	binary.NativeEndian.PutUint16(b[unix.CmsgLen(0):], uint16(segSize))
	return b
}

func TestGroSegmentSize_NoControlData(t *testing.T) {
	assert.Equal(t, 0, groSegmentSize(nil))
	assert.Equal(t, 0, groSegmentSize([]byte{}))
}

func TestGroSegmentSize_ParsesUDPGROMessage(t *testing.T) {
	assert.Equal(t, 1350, groSegmentSize(groOOB(1350)))
}

func TestGroSegmentSize_IgnoresUnrelatedControlMessage(t *testing.T) {
	// A GSO (write-side) cmsg has the same level and length but a different Type; the
	// read-side parser must not mistake it for a UDP_GRO message.
	assert.Equal(t, 0, groSegmentSize(gsoOOB(1350)))
}

// TestSplitGRO_Uncoalesced verifies the overwhelmingly common case -- no UDP_GRO control
// message, or the kernel chose not to coalesce this particular read -- yields exactly
// the message's own payload, unsplit.
func TestSplitGRO_Uncoalesced(t *testing.T) {
	payload := []byte("one datagram, not coalesced")
	msg := &ipv4.Message{Buffers: [][]byte{append([]byte(nil), payload...)}, N: len(payload)}

	var got [][]byte
	splitGRO(msg, func(data []byte) { got = append(got, data) })

	assert.Equal(t, [][]byte{payload}, got)
}

// TestSplitGRO_CoalescedEvenRun verifies a coalesced buffer whose length is an exact
// multiple of the advertised segment size splits into that many equal-sized datagrams.
func TestSplitGRO_CoalescedEvenRun(t *testing.T) {
	segSize := 4
	buf := []byte("aaaabbbbcccc") // three 4-byte segments
	oob := groOOB(segSize)
	msg := &ipv4.Message{Buffers: [][]byte{buf}, N: len(buf), OOB: oob, NN: len(oob)}

	var got [][]byte
	splitGRO(msg, func(data []byte) { got = append(got, append([]byte(nil), data...)) })

	assert.Equal(t, [][]byte{[]byte("aaaa"), []byte("bbbb"), []byte("cccc")}, got)
}

// TestSplitGRO_CoalescedShortLastSegment verifies UDP_GRO's own contract -- every
// segment but possibly the last is exactly segSize, the last may be shorter -- is
// honored on the read side exactly as gsoRunAt already honors it on the write side.
func TestSplitGRO_CoalescedShortLastSegment(t *testing.T) {
	segSize := 4
	buf := []byte("aaaabbbbcc") // two 4-byte segments, one 2-byte remainder
	oob := groOOB(segSize)
	msg := &ipv4.Message{Buffers: [][]byte{buf}, N: len(buf), OOB: oob, NN: len(oob)}

	var got [][]byte
	splitGRO(msg, func(data []byte) { got = append(got, append([]byte(nil), data...)) })

	assert.Equal(t, [][]byte{[]byte("aaaa"), []byte("bbbb"), []byte("cc")}, got)
}

// TestSplitGRO_SegmentSizeNotSmallerThanPayload verifies a (malformed or degenerate)
// segment size that is not actually smaller than the payload is treated as "no split" --
// matching splitGRO's own guard -- rather than looping forever or yielding an empty
// trailing segment.
func TestSplitGRO_SegmentSizeNotSmallerThanPayload(t *testing.T) {
	buf := []byte("aaaa")
	oob := groOOB(len(buf))
	msg := &ipv4.Message{Buffers: [][]byte{buf}, N: len(buf), OOB: oob, NN: len(oob)}

	var got [][]byte
	splitGRO(msg, func(data []byte) { got = append(got, data) })

	assert.Equal(t, [][]byte{buf}, got)
}

// TestNewReadBatchMessages_AllocatesOOBOnlyWhenGRORequested verifies the memory-cost
// side of GRO support is opt-in per socket: a message batch built for a non-GRO socket
// carries no control-message buffer at all.
func TestNewReadBatchMessages_AllocatesOOBOnlyWhenGRORequested(t *testing.T) {
	plain := newReadBatchMessages(2, false)
	for _, m := range plain {
		assert.Nil(t, m.OOB)
	}

	withGRO := newReadBatchMessages(2, true)
	for _, m := range withGRO {
		assert.Len(t, m.OOB, groCmsgSpace)
	}
}

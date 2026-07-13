package quicfwd_test

import (
	"encoding/binary"
	"encoding/hex"
	"net/netip"
	"testing"

	"github.com/quic-go/quic-go/quicvarint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
)

// buildLongHeader constructs a structurally valid (but not encrypted -- SplitCoalesced
// never looks past the invariant/length-carrying fields) QUIC v1 long-header packet of
// the given type, for exercising the coalescing splitter without needing real Initial
// keys.
func buildLongHeader(t *testing.T, typ quicfwd.PacketType, dcid, scid, token, payload []byte) []byte {
	t.Helper()
	b := []byte{0xc0 | byte(typ)<<4}
	var verBuf [4]byte
	binary.BigEndian.PutUint32(verBuf[:], quicfwd.Version1)
	b = append(b, verBuf[:]...)
	b = append(b, byte(len(dcid)))
	b = append(b, dcid...)
	b = append(b, byte(len(scid)))
	b = append(b, scid...)
	if typ == quicfwd.TypeInitial {
		b = quicvarint.Append(b, uint64(len(token)))
		b = append(b, token...)
	}
	if typ != quicfwd.TypeRetry {
		b = quicvarint.Append(b, uint64(len(payload)))
	}
	b = append(b, payload...)
	return b
}

func TestSplitCoalesced_SinglePacket_RFC9001Vector(t *testing.T) {
	full, err := hex.DecodeString(readTestdataHex(t, "rfc9001_a2_client_initial.hex"))
	require.NoError(t, err)
	packets, err := quicfwd.SplitCoalesced(full)
	require.NoError(t, err)
	require.Len(t, packets, 1)
	assert.Equal(t, full, packets[0])
}

func TestSplitCoalesced_TwoLongHeaderPackets(t *testing.T) {
	first := buildLongHeader(t, quicfwd.TypeInitial, []byte{1, 2, 3, 4}, []byte{5, 6}, nil, []byte("first-packet-payload"))
	second := buildLongHeader(t, quicfwd.TypeHandshake, []byte{1, 2, 3, 4}, []byte{5, 6}, nil, []byte("second-packet-payload"))
	datagram := append(append([]byte(nil), first...), second...)

	packets, err := quicfwd.SplitCoalesced(datagram)
	require.NoError(t, err)
	require.Len(t, packets, 2)
	assert.Equal(t, first, packets[0])
	assert.Equal(t, second, packets[1])
}

func TestSplitCoalesced_ShortHeaderIsLastAndEndsSplit(t *testing.T) {
	ip := netip.MustParseAddr("10.1.2.3")
	cid, err := quicfwd.EncodeCID(ip)
	require.NoError(t, err)

	first := buildLongHeader(t, quicfwd.TypeInitial, []byte{9, 9}, []byte{8, 8}, nil, []byte("initial-payload"))
	short := append([]byte{0x40}, cid...)
	short = append(short, []byte("1-rtt-payload-goes-to-end")...)
	datagram := append(append([]byte(nil), first...), short...)

	packets, err := quicfwd.SplitCoalesced(datagram)
	require.NoError(t, err)
	require.Len(t, packets, 2)
	assert.Equal(t, first, packets[0])
	assert.Equal(t, short, packets[1])
}

func TestSplitCoalesced_RetryConsumesRemainder(t *testing.T) {
	retry := buildLongHeader(t, quicfwd.TypeRetry, []byte{1}, []byte{2}, nil, []byte("retry-token-and-integrity-tag"))
	// Even with trailing bytes appended (as if something followed), a Retry has no
	// Length field, so nothing can legitimately follow it and the whole remainder
	// is reported as this one packet.
	packets, err := quicfwd.SplitCoalesced(retry)
	require.NoError(t, err)
	require.Len(t, packets, 1)
	assert.Equal(t, retry, packets[0])
}

func TestSplitCoalesced_UnknownVersionConsumesRemainder(t *testing.T) {
	b := []byte{0xc0, 0xde, 0xad, 0xbe, 0xef, 0x04, 1, 2, 3, 4, 0x02, 5, 6, 0xaa, 0xbb, 0xcc}
	packets, err := quicfwd.SplitCoalesced(b)
	require.NoError(t, err)
	require.Len(t, packets, 1)
	assert.Equal(t, b, packets[0])
}

func TestSplitCoalesced_TruncatedLengthIsError(t *testing.T) {
	// A well-formed prefix (version, DCID, SCID) but truncated before the
	// length-declared region can be satisfied.
	full := buildLongHeader(t, quicfwd.TypeHandshake, []byte{1}, []byte{2}, nil, []byte("some-payload"))
	truncated := full[:len(full)-5]
	_, err := quicfwd.SplitCoalesced(truncated)
	assert.Error(t, err)
}

func TestSplitCoalesced_EmptyDatagram(t *testing.T) {
	packets, err := quicfwd.SplitCoalesced(nil)
	require.NoError(t, err)
	assert.Empty(t, packets)
}

package quicfwd_test

import (
	"encoding/hex"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
)

func TestParsePacket_ShortHeader(t *testing.T) {
	ip := netip.MustParseAddr("10.1.2.3")
	cid, err := quicfwd.EncodeCID(ip)
	require.NoError(t, err)

	b := append([]byte{0x40}, cid...) // header form bit clear, fixed bit set
	b = append(b, []byte{0xaa, 0xbb}...)

	info, err := quicfwd.ParsePacket(b)
	require.NoError(t, err)
	assert.Equal(t, quicfwd.KindShortHeader, info.Kind)
	assert.Equal(t, cid, info.DCID)
	assert.Nil(t, info.SCID)
}

func TestParsePacket_ShortHeaderTooShort(t *testing.T) {
	_, err := quicfwd.ParsePacket([]byte{0x40, 0x01, 0x02})
	assert.Error(t, err)
}

func TestParsePacket_LongHeader(t *testing.T) {
	// Hand-built long header: header form + fixed bit + Initial type (00) + PN
	// length bits, version 1, an 8-byte DCID, a 4-byte SCID, then some
	// version-specific bytes we don't care about for this test.
	b := []byte{
		0xc3, 0x00, 0x00, 0x00, 0x01, // long header, fixed bit, Initial type, version 1
		0x08, 1, 2, 3, 4, 5, 6, 7, 8, // DCID length + 8-byte DCID
		0x04, 9, 10, 11, 12, // SCID length + 4-byte SCID
		0x00, 0x00, 0x00, 0x00, // filler so later fields don't panic
	}

	info, err := quicfwd.ParsePacket(b)
	require.NoError(t, err)
	assert.Equal(t, quicfwd.KindLongHeader, info.Kind)
	assert.Equal(t, quicfwd.Version1, info.Version)
	assert.Equal(t, quicfwd.TypeInitial, info.Type)
	assert.Equal(t, []byte{1, 2, 3, 4, 5, 6, 7, 8}, info.DCID)
	assert.Equal(t, []byte{9, 10, 11, 12}, info.SCID)
}

func TestParsePacket_LongHeaderUnknownVersionReportedNotErrored(t *testing.T) {
	// A long header packet of a version this package doesn't know about must
	// still parse successfully -- DCID/SCID extraction relies only on the
	// RFC 8999 invariants, not on knowing the version -- but Type must come back
	// TypeUnknown so a caller can't misinterpret the type bits.
	b := []byte{
		0xff, 0xde, 0xad, 0xbe, 0xef, // long header, unrecognized version 0xdeadbeef
		0x04, 1, 2, 3, 4, // DCID length + 4-byte DCID
		0x04, 5, 6, 7, 8, // SCID length + 4-byte SCID
	}

	info, err := quicfwd.ParsePacket(b)
	require.NoError(t, err)
	assert.Equal(t, quicfwd.KindLongHeader, info.Kind)
	assert.Equal(t, uint32(0xdeadbeef), info.Version)
	assert.Equal(t, quicfwd.TypeUnknown, info.Type)
}

func TestParsePacket_VersionNegotiation(t *testing.T) {
	b := []byte{
		0x80, 0x00, 0x00, 0x00, 0x00, // long header, version 0 (Version Negotiation)
		0x04, 1, 2, 3, 4, // DCID length + 4-byte DCID
		0x04, 5, 6, 7, 8, // SCID length + 4-byte SCID
		0x00, 0x00, 0x00, 0x01, // one supported version
	}

	info, err := quicfwd.ParsePacket(b)
	require.NoError(t, err)
	assert.Equal(t, quicfwd.KindVersionNegotiation, info.Kind)
	assert.Equal(t, uint32(0), info.Version)
}

func TestParsePacket_Empty(t *testing.T) {
	_, err := quicfwd.ParsePacket(nil)
	assert.Error(t, err)
}

func TestParsePacket_LongHeaderTruncated(t *testing.T) {
	tests := map[string][]byte{
		"no version":     {0xc0},
		"no DCID length": {0xc0, 0x00, 0x00, 0x00, 0x01},
		"DCID cut short": {0xc0, 0x00, 0x00, 0x00, 0x01, 0x08, 0x01, 0x02},
		"no SCID length": append([]byte{0xc0, 0x00, 0x00, 0x00, 0x01, 0x02}, []byte{1, 2}...),
		"SCID cut short": append([]byte{0xc0, 0x00, 0x00, 0x00, 0x01, 0x00, 0x02}, []byte{1}...),
	}
	for name, b := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := quicfwd.ParsePacket(b)
			assert.Error(t, err)
		})
	}
}

// TestParsePacket_RFC9001Vector cross-checks ParsePacket's version-independent
// classification against the RFC 9001 Appendix A.2 client Initial packet header (before
// header protection is even considered): version 1, 8-byte DCID 8394c8f03e515708, empty
// SCID.
func TestParsePacket_RFC9001Vector(t *testing.T) {
	b, err := hex.DecodeString(readTestdataHex(t, "rfc9001_a2_client_initial.hex"))
	require.NoError(t, err)

	info, err := quicfwd.ParsePacket(b)
	require.NoError(t, err)
	assert.Equal(t, quicfwd.KindLongHeader, info.Kind)
	assert.Equal(t, quicfwd.Version1, info.Version)
	assert.Equal(t, quicfwd.TypeInitial, info.Type)
	dcid, err := hex.DecodeString("8394c8f03e515708")
	require.NoError(t, err)
	assert.Equal(t, dcid, info.DCID)
	assert.Empty(t, info.SCID)
}

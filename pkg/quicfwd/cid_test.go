package quicfwd_test

import (
	"net/netip"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
)

func TestEncodeDecodeCID_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ip   netip.Addr
	}{
		{"IPv4", netip.MustParseAddr("10.42.1.7")},
		{"IPv6", netip.MustParseAddr("fd00:abcd::1")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cid, err := quicfwd.EncodeCID(tt.ip)
			require.NoError(t, err)
			require.Len(t, cid, quicfwd.CIDLen)

			got, ok := quicfwd.DecodeCID(cid)
			require.True(t, ok)
			assert.Equal(t, tt.ip.Unmap(), got)
		})
	}
}

func TestEncodeCID_DistinctEachCall(t *testing.T) {
	ip := netip.MustParseAddr("10.42.1.7")
	a, err := quicfwd.EncodeCID(ip)
	require.NoError(t, err)
	b, err := quicfwd.EncodeCID(ip)
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "two CIDs for the same IP must not collide")
}

func TestEncodeCID_RejectsInvalidAddresses(t *testing.T) {
	tests := []netip.Addr{
		{},
		netip.IPv4Unspecified(),
		netip.IPv6Unspecified(),
		netip.MustParseAddr("224.0.0.1"), // IPv4 multicast
		netip.MustParseAddr("ff02::1"),   // IPv6 multicast
	}
	for _, ip := range tests {
		_, err := quicfwd.EncodeCID(ip)
		assert.Error(t, err, "should reject %s", ip)
	}
}

func TestDecodeCID_RejectsWrongLength(t *testing.T) {
	for _, n := range []int{0, 1, 8, 19, 21, 32} {
		_, ok := quicfwd.DecodeCID(make([]byte, n))
		assert.False(t, ok, "length %d should be rejected", n)
	}
}

func TestDecodeCID_RejectsWrongMagic(t *testing.T) {
	cid, err := quicfwd.EncodeCID(netip.MustParseAddr("10.0.0.1"))
	require.NoError(t, err)
	cid[0] ^= 0xff
	_, ok := quicfwd.DecodeCID(cid)
	assert.False(t, ok)
}

func TestDecodeCID_RejectsRandomBytes(t *testing.T) {
	// A fixed, arbitrary 20-byte buffer that is not of our format.
	random := []byte{
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
		0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14,
	}
	_, ok := quicfwd.DecodeCID(random)
	assert.False(t, ok)
}

func TestDecodeCID_RejectsAllZero(t *testing.T) {
	_, ok := quicfwd.DecodeCID(make([]byte, quicfwd.CIDLen))
	assert.False(t, ok)
}

func TestDecodeCID_RejectsUnrecognizedFamily(t *testing.T) {
	cid, err := quicfwd.EncodeCID(netip.MustParseAddr("10.0.0.1"))
	require.NoError(t, err)
	cid[1] = 0x7f // neither familyIPv4 nor familyIPv6
	_, ok := quicfwd.DecodeCID(cid)
	assert.False(t, ok)
}

func TestCIDGenerator_ImplementsQuicGoInterface(t *testing.T) {
	var _ quic.ConnectionIDGenerator = quicfwd.NewCIDGenerator(netip.MustParseAddr("10.0.0.1"))
}

func TestCIDGenerator_GeneratesRoutableCIDs(t *testing.T) {
	ip := netip.MustParseAddr("192.168.1.55")
	gen := quicfwd.NewCIDGenerator(ip)
	assert.Equal(t, quicfwd.CIDLen, gen.ConnectionIDLen())

	cid, err := gen.GenerateConnectionID()
	require.NoError(t, err)
	assert.Equal(t, quicfwd.CIDLen, cid.Len())

	got, ok := quicfwd.DecodeCID(cid.Bytes())
	require.True(t, ok)
	assert.Equal(t, ip, got)
}

package quicfwd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rfc9001ClientHelloBytes returns the 241-byte TLS ClientHello handshake message
// (header + body) from the RFC 9001 Appendix A.2 CRYPTO frame, by decrypting the real
// vector and walking its frames -- so these whitebox tests exercise real ClientHello
// bytes, not a hand-built approximation of one.
func rfc9001ClientHelloBytes(t *testing.T) []byte {
	t.Helper()
	b := readHexFile(t, "rfc9001_a2_client_initial.hex")
	fields, err := parseLongHeaderFields(b)
	require.NoError(t, err)
	payload, err := removeHeaderProtectionAndDecrypt(b, fields)
	require.NoError(t, err)
	segments, err := walkInitialFrames(payload)
	require.NoError(t, err)
	require.Len(t, segments, 1)
	require.Equal(t, uint64(0), segments[0].offset)
	return segments[0].data
}

func TestParseClientHelloSNI_RFC9001Vector(t *testing.T) {
	sni, ok, err := parseClientHelloSNI(rfc9001ClientHelloBytes(t))
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "example.com", sni)
}

// TestParseClientHelloSNI_Truncated checks that cutting the same real ClientHello off
// at various points -- simulating the first packet of a ClientHello that continues into
// a second Initial packet -- always yields ok=false with no error, never a panic or a
// false parse.
func TestParseClientHelloSNI_Truncated(t *testing.T) {
	full := rfc9001ClientHelloBytes(t)
	cutPoints := []int{0, 1, 2, 3, 4, 10, 38, 39, 40, 41, 70, 100, len(full) - 1}
	for _, n := range cutPoints {
		if n > len(full) {
			continue
		}
		_, ok, err := parseClientHelloSNI(full[:n])
		assert.NoError(t, err, "cut at %d", n)
		assert.False(t, ok, "cut at %d should be incomplete", n)
	}
}

func TestParseClientHelloSNI_WrongMessageType(t *testing.T) {
	full := rfc9001ClientHelloBytes(t)
	bad := append([]byte(nil), full...)
	bad[0] = 0x02 // ServerHello, not ClientHello
	_, ok, err := parseClientHelloSNI(bad)
	assert.False(t, ok)
	assert.Error(t, err)
}

func TestReassembleFromZero_ContinuationFragmentHasNoOffsetZero(t *testing.T) {
	// A continuation fragment: CRYPTO data starting at a non-zero stream offset,
	// with no handshake header in this packet at all. ExtractSNI must treat this
	// as "not yet", not as an error -- the caller's handshake cache is what
	// resolves it.
	segments := []cryptoSegment{{offset: 200, data: []byte("continuation")}}
	_, ok := reassembleFromZero(segments)
	assert.False(t, ok)
}

func TestReassembleFromZero_MergesContiguousSegments(t *testing.T) {
	segments := []cryptoSegment{
		{offset: 3, data: []byte("defg")},
		{offset: 0, data: []byte("abc")},
	}
	data, ok := reassembleFromZero(segments)
	require.True(t, ok)
	assert.Equal(t, "abcdefg", string(data))
}

func TestReassembleFromZero_StopsAtGap(t *testing.T) {
	segments := []cryptoSegment{
		{offset: 0, data: []byte("abc")},
		{offset: 10, data: []byte("xyz")}, // gap between offset 3 and 10
	}
	data, ok := reassembleFromZero(segments)
	require.True(t, ok)
	assert.Equal(t, "abc", string(data))
}

func TestReassembleFromZero_NoSegments(t *testing.T) {
	_, ok := reassembleFromZero(nil)
	assert.False(t, ok)
}

func TestWalkInitialFrames_PaddingAndPing(t *testing.T) {
	payload := []byte{0x00, 0x00, 0x01, 0x00}
	segments, err := walkInitialFrames(payload)
	require.NoError(t, err)
	assert.Empty(t, segments)
}

func TestWalkInitialFrames_RejectsIllegalFrameType(t *testing.T) {
	// STREAM (0x08) is not a legal frame type in an Initial packet (RFC 9000
	// §12.4).
	_, err := walkInitialFrames([]byte{0x08, 0x00, 0x00})
	assert.Error(t, err)
}

func TestWalkInitialFrames_SkipsAckFrame(t *testing.T) {
	// A minimal ACK frame: Largest Acked=5, Delay=0, Range Count=0, First Range=0,
	// followed by a CRYPTO frame at offset 0 with 2 bytes of data.
	payload := []byte{
		0x02, 0x05, 0x00, 0x00, 0x00, // ACK: largest=5, delay=0, range count=0, first range=0
		0x06, 0x00, 0x02, 0xaa, 0xbb, // CRYPTO: offset=0, length=2, data
	}
	segments, err := walkInitialFrames(payload)
	require.NoError(t, err)
	require.Len(t, segments, 1)
	assert.Equal(t, []byte{0xaa, 0xbb}, segments[0].data)
}

func TestWalkInitialFrames_SkipsConnectionCloseFrame(t *testing.T) {
	// CONNECTION_CLOSE (0x1c): Error Code=0, Frame Type=0, Reason Length=0.
	payload := []byte{0x1c, 0x00, 0x00, 0x00}
	segments, err := walkInitialFrames(payload)
	require.NoError(t, err)
	assert.Empty(t, segments)
}

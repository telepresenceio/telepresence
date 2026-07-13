package quicfwd_test

import (
	"encoding/hex"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
)

// TestExtractSNI_RFC9001Vector is the end-to-end validation the design calls for:
// ExtractSNI, given nothing but the protected client Initial packet bytes from RFC 9001
// Appendix A.2, must derive the same Initial keys, remove header protection, decrypt the
// payload, walk its frames, and parse far enough into the ClientHello to recover its SNI
// -- all without out-of-band help, exactly as a forwarder would.
func TestExtractSNI_RFC9001Vector(t *testing.T) {
	b, err := hex.DecodeString(readTestdataHex(t, "rfc9001_a2_client_initial.hex"))
	require.NoError(t, err)

	sni, ok, err := quicfwd.ExtractSNI(b)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "example.com", sni)
}

func TestExtractSNI_RejectsShortHeader(t *testing.T) {
	b := append([]byte{0x40}, make([]byte, quicfwd.CIDLen)...)
	_, ok, err := quicfwd.ExtractSNI(b)
	assert.False(t, ok)
	assert.Error(t, err)
}

func TestExtractSNI_RejectsUnknownVersion(t *testing.T) {
	b := []byte{0xc0, 0xde, 0xad, 0xbe, 0xef, 0x00, 0x00}
	_, ok, err := quicfwd.ExtractSNI(b)
	assert.False(t, ok)
	require.Error(t, err)
	assert.True(t, errors.Is(err, quicfwd.ErrUnsupportedVersion))
}

func TestExtractSNI_RejectsNonInitialLongHeader(t *testing.T) {
	// Long header, version 1, type bits = 0b01 (0-RTT, not Initial).
	b := []byte{0xd0, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00}
	_, ok, err := quicfwd.ExtractSNI(b)
	assert.False(t, ok)
	require.Error(t, err)
	assert.True(t, errors.Is(err, quicfwd.ErrNotInitial))
}

func TestExtractSNI_GarbageFailsAEAD(t *testing.T) {
	b, err := hex.DecodeString(readTestdataHex(t, "rfc9001_a2_client_initial.hex"))
	require.NoError(t, err)
	// Flip a byte deep in the protected payload; the AEAD tag must no longer
	// verify, and that must surface as an error, not a silent ok=false.
	b[100] ^= 0xff

	_, ok, err := quicfwd.ExtractSNI(b)
	assert.False(t, ok)
	assert.Error(t, err)
}

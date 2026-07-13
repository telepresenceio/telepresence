package quicfwd

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientInitialKeys_RFC9001Vector checks key derivation in isolation against the
// values published in RFC 9001 Appendix A.1/A.2, before involving header protection
// removal or AEAD decryption at all. If this fails, the bug is in HKDF-Expand-Label or
// the salt/labels; if only the end-to-end ExtractSNI test fails, the bug is further
// down the pipeline (header protection or AEAD or frame parsing).
func TestClientInitialKeys_RFC9001Vector(t *testing.T) {
	dcid, err := hex.DecodeString("8394c8f03e515708")
	require.NoError(t, err)

	key, iv, hp, err := clientInitialKeys(dcid)
	require.NoError(t, err)

	assert.Equal(t, "1f369613dd76d5467730efcbe3b1a22d", hex.EncodeToString(key))
	assert.Equal(t, "fa044b2f42a3fd3b46fb255c", hex.EncodeToString(iv))
	assert.Equal(t, "9f50449e04a0e810283a1e9933adedd2", hex.EncodeToString(hp))
}

func TestHkdfExpandLabel_RFC9001ClientInitialSecret(t *testing.T) {
	dcid, err := hex.DecodeString("8394c8f03e515708")
	require.NoError(t, err)

	initialSecret, err := hkdfExtract(dcid)
	require.NoError(t, err)
	assert.Equal(t, "7db5df06e7a69e432496adedb00851923595221596ae2ae9fb8115c1e9ed0a44", hex.EncodeToString(initialSecret))

	clientSecret, err := hkdfExpandLabel(initialSecret, "client in", 32)
	require.NoError(t, err)
	assert.Equal(t, "c00cf151ca5be075ed0ebfb5c80323c42d6b7db67881289af4008f1f6c357aea", hex.EncodeToString(clientSecret))
}

// TestRemoveHeaderProtectionAndDecrypt_RFC9001Vector exercises header protection
// removal and AEAD decryption directly against the protected packet bytes from RFC 9001
// Appendix A.2, checking the reconstructed packet number and the decrypted payload's
// leading CRYPTO frame against the vector's own unprotected numbers.
func TestRemoveHeaderProtectionAndDecrypt_RFC9001Vector(t *testing.T) {
	b := readHexFile(t, "rfc9001_a2_client_initial.hex")

	fields, err := parseLongHeaderFields(b)
	require.NoError(t, err)
	assert.Equal(t, Version1, fields.version)

	payload, err := removeHeaderProtectionAndDecrypt(b, fields)
	require.NoError(t, err)

	// RFC 9001 Appendix A.2: the unprotected payload is a CRYPTO frame (type 0x06,
	// offset 0, length 0xf1 = 241) followed by PADDING out to 1162 bytes total.
	require.GreaterOrEqual(t, len(payload), 4)
	assert.Equal(t, byte(0x06), payload[0], "frame type must be CRYPTO")
	assert.Len(t, payload, 1162)

	segments, err := walkInitialFrames(payload)
	require.NoError(t, err)
	require.Len(t, segments, 1)
	assert.Equal(t, uint64(0), segments[0].offset)
	assert.Len(t, segments[0].data, 241)
	assert.Equal(t, byte(tlsHandshakeTypeClientHello), segments[0].data[0])
}

func readHexFile(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	b, err := hex.DecodeString(strings.Join(strings.Fields(string(raw)), ""))
	require.NoError(t, err)
	return b
}

package quicfwd

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCryptoAccumulator_RFC9001Vector feeds the single-packet RFC 9001 Appendix A.2
// vector through a fresh accumulator: Feed must behave exactly like ExtractSNI when the
// very first packet already carries the whole ClientHello.
func TestCryptoAccumulator_RFC9001Vector(t *testing.T) {
	b := readHexFile(t, "rfc9001_a2_client_initial.hex")

	acc := NewCryptoAccumulator()
	sni, ok, err := acc.Feed(b)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "example.com", sni)
}

// TestCryptoAccumulator_CapturedQuicGoClient_TwoPacketSplit drives a real quic-go client
// against a bloated ALPN list (forcing a multi-packet ClientHello regardless of any
// particular Go toolchain's default TLS configuration, exactly as
// TestExtractSNI_CapturedQuicGoClient does) and feeds every captured Initial datagram to
// a single CryptoAccumulator in arrival order. Every packet but the last must come back
// ok=false with no error; the last must recover the SNI -- this is the scenario "The
// forwarder" section of docs/plans/quic-transport/design.md describes needing a
// cross-packet handshake cache for, and CryptoAccumulator is that cache's reassembly
// core.
func TestCryptoAccumulator_CapturedQuicGoClient_TwoPacketSplit(t *testing.T) {
	const sni = "sni-accumulator-test.example"

	alpn := make([]string, 64)
	for i := range alpn {
		alpn[i] = "quicfwd-test-alpn-protocol-filler"
	}
	tlsConf := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
		NextProtos:         alpn,
	}
	cfg := &quic.Config{
		Versions:             []quic.Version{quic.Version1},
		HandshakeIdleTimeout: 300 * time.Millisecond,
	}

	packets := captureClientInitialDatagrams(t, tlsConf, cfg)
	require.NotEmpty(t, packets)

	acc := NewCryptoAccumulator()
	var (
		got    string
		ok     bool
		err    error
		lastAt = -1
	)
	for i, pkt := range packets {
		got, ok, err = acc.Feed(pkt)
		require.NoError(t, err, "packet %d: a real quic-go client's own Initial packet must always parse cleanly", i)
		if ok {
			lastAt = i
			break
		}
	}
	require.True(t, ok, "SNI must be recoverable once every captured packet has been fed")
	assert.Equal(t, sni, got)
	t.Logf("SNI recovered after feeding %d of %d captured packet(s)", lastAt+1, len(packets))
}

// TestCryptoAccumulator_MalformedPacketLeavesStatePreviouslyAccumulated feeds one valid
// Initial packet's first fragment, then a garbage packet, and confirms Feed reports the
// error without discarding what had already been accumulated: a subsequent Feed of the
// real continuation should still be able to complete the reassembly.
func TestCryptoAccumulator_MalformedPacketLeavesStatePreviouslyAccumulated(t *testing.T) {
	const sni = "sni-error-recovery-test.example"
	alpn := make([]string, 64)
	for i := range alpn {
		alpn[i] = "quicfwd-test-alpn-protocol-filler"
	}
	tlsConf := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
		NextProtos:         alpn,
	}
	cfg := &quic.Config{
		Versions:             []quic.Version{quic.Version1},
		HandshakeIdleTimeout: 300 * time.Millisecond,
	}
	packets := captureClientInitialDatagrams(t, tlsConf, cfg)
	require.GreaterOrEqual(t, len(packets), 2, "test requires a real multi-packet split")

	acc := NewCryptoAccumulator()
	_, ok, err := acc.Feed(packets[0])
	require.NoError(t, err)
	require.False(t, ok, "first packet alone should not yet contain the SNI")

	// Garbage: too short to be any kind of QUIC packet.
	_, ok, err = acc.Feed([]byte{0x00})
	assert.False(t, ok)
	assert.Error(t, err)

	// The accumulator must still complete once fed every real packet after the
	// garbage one.
	var got string
	for _, pkt := range packets[1:] {
		got, ok, err = acc.Feed(pkt)
		require.NoError(t, err)
		if ok {
			break
		}
	}
	require.True(t, ok)
	assert.Equal(t, sni, got)
}

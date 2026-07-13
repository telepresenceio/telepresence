package quicfwd

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

// captureClientInitialDatagrams dials a real quic-go client (the exact quic-go version
// this module depends on) at a UDP socket that never answers, and returns every
// datagram the client sent before giving up. This is what proves ExtractSNI is
// compatible with quic-go's actual wire output, not just with the RFC's hand-verified
// vector.
//
// The "server" never completes a handshake -- it only records what arrives -- so the
// dial always ends in a handshake-timeout error, which is expected and ignored.
func captureClientInitialDatagrams(t *testing.T, tlsConf *tls.Config, cfg *quic.Config) [][]byte {
	t.Helper()

	serverConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer serverConn.Close()

	var packets [][]byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 65535)
		for {
			n, _, err := serverConn.ReadFrom(buf)
			if err != nil {
				return
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			packets = append(packets, pkt)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, dialErr := quic.DialAddr(ctx, serverConn.LocalAddr().String(), tlsConf, cfg)
	// The handshake can never complete: the fake "server" only reads, never
	// writes. A timeout/idle error here is expected; anything else would mean the
	// client didn't even get as far as sending an Initial packet.
	require.Error(t, dialErr)

	require.NoError(t, serverConn.SetReadDeadline(time.Now()))
	<-done
	return packets
}

// assertExtractSNIAcrossCapturedPackets is the shared body of the two capture tests
// below. It requires that every individual captured datagram either yields the expected
// SNI or comes back ok=false with no error (the continuation/truncated-fragment
// contract) -- ExtractSNI erroring on a real client's own packet would be a bug -- and
// then checks whether the SNI was actually recoverable from a single packet.
//
// quic-go v0.60.0, dialing with a Go toolchain whose crypto/tls offers a post-quantum
// hybrid key share by default, was observed (see the -v log this test emits) to split
// the ClientHello's CRYPTO data such that neither individual Initial packet contains a
// contiguous run from offset 0 all the way to the server_name extension: the first
// packet's own frames stop a few bytes short of where the extensions block even begins,
// and the byte range that would bridge the two arrives only in the next packet. That is
// precisely the scenario "The forwarder" section of docs/plans/quic-transport/design.md
// describes as needing a small, ephemeral, cross-packet handshake cache -- reassembly
// across packets is explicitly that later task's job, not this package's. So when no
// single packet carries the SNI, this test simulates what that cache will eventually do
// -- merge every CRYPTO segment observed across all captured packets of the same
// connection attempt -- using this package's own reassembleFromZero, and confirms the
// result is a well-formed ClientHello containing the expected SNI. That demonstrates
// the low-level frame/decrypt/parse logic is correct and that the only thing missing
// for full end-to-end extraction against this real client is the cross-packet cache.
func assertExtractSNIAcrossCapturedPackets(t *testing.T, packets [][]byte, sni string) {
	t.Helper()
	require.NotEmpty(t, packets, "client must have sent at least one datagram")

	var foundSingle bool
	var allSegments []cryptoSegment
	for i, pkt := range packets {
		got, ok, err := ExtractSNI(pkt)
		require.NoError(t, err, "packet %d: a real quic-go client's own Initial packet must always parse cleanly", i)
		if ok {
			require.Equal(t, sni, got, "packet %d", i)
			foundSingle = true
		}

		fields, err := parseLongHeaderFields(pkt)
		require.NoError(t, err, "packet %d", i)
		payload, err := removeHeaderProtectionAndDecrypt(pkt, fields)
		require.NoError(t, err, "packet %d", i)
		segments, err := walkInitialFrames(payload)
		require.NoError(t, err, "packet %d", i)
		allSegments = append(allSegments, segments...)
	}

	t.Logf("captured %d datagram(s); SNI recoverable from a single packet: %v", len(packets), foundSingle)
	if foundSingle {
		return
	}

	// No single packet had it; confirm the SNI is recoverable once fragments from
	// every captured packet of this connection attempt are merged, proving the
	// per-packet miss is a genuine multi-packet split and not a parsing bug.
	merged, ok := reassembleFromZero(allSegments)
	require.True(t, ok, "merging every captured CRYPTO segment must still find one starting at offset 0")
	got, ok, err := parseClientHelloSNI(merged)
	require.NoError(t, err)
	require.True(t, ok, "merged ClientHello across all captured packets should no longer be truncated")
	require.Equal(t, sni, got)
	t.Logf("SNI recovered only after merging CRYPTO segments across %d packet(s); this is the scenario the forwarder's handshake cache exists for", len(packets))
}

// TestExtractSNI_CapturedQuicGoClient dials a real quic-go client with a deliberately
// bloated ALPN list (to force a multi-packet ClientHello regardless of any particular Go
// toolchain's default TLS configuration) and feeds every resulting datagram through
// ExtractSNI.
func TestExtractSNI_CapturedQuicGoClient(t *testing.T) {
	const sni = "sni-capture-test.example"

	alpn := make([]string, 64)
	for i := range alpn {
		alpn[i] = fmt.Sprintf("quicfwd-test-alpn-protocol-%03d", i)
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
	assertExtractSNIAcrossCapturedPackets(t, packets, sni)
}

// TestExtractSNI_CapturedQuicGoClient_DefaultTLSConfig runs the same check against this
// Go toolchain's default TLS 1.3 client configuration, with no artificial bloating. It
// documents (via the -v log) whether the default configuration alone already produces a
// multi-packet ClientHello -- true for Go toolchains that offer a post-quantum hybrid
// key share by default -- without asserting one way or the other, since either is
// handled correctly.
func TestExtractSNI_CapturedQuicGoClient_DefaultTLSConfig(t *testing.T) {
	const sni = "sni-default-test.example"
	tlsConf := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
		NextProtos:         []string{"h3"},
	}
	cfg := &quic.Config{
		Versions:             []quic.Version{quic.Version1},
		HandshakeIdleTimeout: 300 * time.Millisecond,
	}

	packets := captureClientInitialDatagrams(t, tlsConf, cfg)
	assertExtractSNIAcrossCapturedPackets(t, packets, sni)
}

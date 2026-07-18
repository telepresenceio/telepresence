package quicfwd

import (
	"fmt"
	"sort"

	"golang.org/x/crypto/cryptobyte"
)

// TLS constants needed to reach the server_name extension of a ClientHello. These are
// TLS 1.3 (RFC 8446) and SNI (RFC 6066) wire values, not QUIC-specific.
const (
	tlsHandshakeTypeClientHello = 1
	tlsExtensionServerName      = 0
	tlsServerNameTypeHostName   = 0
)

// ExtractSNI reads the SNI (server_name) sent in the ClientHello carried by a QUIC v1
// Initial packet, without terminating the connection. This is the mechanism "The
// forwarder" section of docs/reference/quic-transport-architecture.md relies on to route a
// connection's first packet: the SNI is readable this way because Initial packets are
// protected with keys derived from public, connection-visible material (RFC 9001 §5.2),
// not from anything secret to the client and server.
//
// ok is false, with a nil error, whenever initialPacket is well-formed but doesn't (yet)
// contain the SNI:
//   - it is a ClientHello continuation fragment (its CRYPTO data starts at a non-zero
//     stream offset, so there is no handshake header at all in this packet) -- the
//     caller is expected to correlate these by client DCID + source address in its own
//     ephemeral handshake cache, exactly as the design document describes;
//   - the ClientHello is truncated before reaching the server_name extension, whether
//     within the handshake header, the fixed ClientHello fields, or the extensions list
//     -- a multi-packet ClientHello whose later packet(s) haven't arrived yet.
//
// A non-nil error means initialPacket itself is malformed: it doesn't parse as a QUIC
// long-header packet, its version isn't one this package has Initial keys for
// (ErrUnsupportedVersion), it isn't an Initial packet (ErrNotInitial), header removal or
// AEAD decryption failed (wrong keys, corrupt data, or non-QUIC junk on the port), or
// its frames don't parse as a legal Initial packet's frames.
func ExtractSNI(initialPacket []byte) (sni string, ok bool, err error) {
	segments, err := decryptInitialCrypto(initialPacket)
	if err != nil {
		return "", false, err
	}
	data, ok := reassembleFromZero(segments)
	if !ok {
		// No CRYPTO frame starts at offset 0: either this packet carries no
		// handshake data at all, or it is a continuation fragment. Either way the
		// caller's handshake cache, not this function, is what correlates it back
		// to a ClientHello.
		return "", false, nil
	}
	return parseClientHelloSNI(data)
}

// decryptInitialCrypto verifies initialPacket is a QUIC v1 Initial packet, removes its
// header protection, AEAD-decrypts it, and returns its CRYPTO frame segments. It is the
// shared core of ExtractSNI (single packet) and CryptoAccumulator.Feed (reassembly
// across the several Initial packets a multi-packet ClientHello is split over).
func decryptInitialCrypto(initialPacket []byte) ([]cryptoSegment, error) {
	info, err := ParsePacket(initialPacket)
	if err != nil {
		return nil, err
	}
	if info.Kind != KindLongHeader {
		return nil, fmt.Errorf("quicfwd: %s packet has no ClientHello to extract", info.Kind)
	}
	if info.Version != Version1 {
		return nil, fmt.Errorf("%w: 0x%08x", ErrUnsupportedVersion, info.Version)
	}
	if info.Type != TypeInitial {
		return nil, fmt.Errorf("%w: got %s", ErrNotInitial, info.Type)
	}

	// ParsePacket already validated the version-independent prefix; re-derive the
	// version-specific offset it also computed rather than threading it through
	// PacketInfo, which is meant to stay a routing-only, version-oblivious type.
	fields, err := parseLongHeaderFields(initialPacket)
	if err != nil {
		return nil, err
	}
	payload, err := removeHeaderProtectionAndDecrypt(initialPacket, fields)
	if err != nil {
		return nil, err
	}
	return walkInitialFrames(payload)
}

// reassembleFromZero returns the longest contiguous run of CRYPTO data starting at
// stream offset 0 across every CRYPTO frame found in a single packet. In practice a
// packet almost always carries exactly one CRYPTO frame, but RFC 9000 doesn't forbid a
// sender from splitting one packet's contribution across several frames, so segments
// are sorted and merged rather than assumed to be a single entry at index 0.
func reassembleFromZero(segments []cryptoSegment) ([]byte, bool) {
	if len(segments) == 0 {
		return nil, false
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].offset < segments[j].offset })
	if segments[0].offset != 0 {
		return nil, false
	}
	data := append([]byte(nil), segments[0].data...)
	next := uint64(len(data))
	for _, seg := range segments[1:] {
		if seg.offset > next {
			break // gap: stop at the longest contiguous prefix we actually have
		}
		if seg.offset < next {
			// Overlap with already-collected data; keep only the new tail.
			overlap := next - seg.offset
			if overlap >= uint64(len(seg.data)) {
				continue
			}
			data = append(data, seg.data[overlap:]...)
		} else {
			data = append(data, seg.data...)
		}
		next = uint64(len(data))
	}
	return data, true
}

// parseClientHelloSNI parses a (possibly truncated) TLS ClientHello handshake message
// starting at data[0], stopping as soon as it has read the server_name extension. Any
// structural inconsistency that unambiguously indicates truncation -- a length prefix
// promising bytes that aren't there -- is reported as ok == false, not an error: that is
// exactly what a multi-packet ClientHello's first packet looks like.
func parseClientHelloSNI(data []byte) (string, bool, error) {
	hs := cryptobyte.String(data)

	var msgType uint8
	if !hs.ReadUint8(&msgType) {
		return "", false, nil
	}
	if msgType != tlsHandshakeTypeClientHello {
		return "", false, fmt.Errorf("quicfwd: unexpected TLS handshake message type %d at CRYPTO offset 0", msgType)
	}
	var body cryptobyte.String
	if !hs.ReadUint24LengthPrefixed(&body) {
		return "", false, nil
	}

	// ClientHello (RFC 8446 §4.1.2): legacy_version, random, legacy_session_id,
	// cipher_suites, legacy_compression_methods, extensions.
	var legacyVersion uint16
	if !body.ReadUint16(&legacyVersion) {
		return "", false, nil
	}
	if !body.Skip(32) { // random
		return "", false, nil
	}
	var sessionID cryptobyte.String
	if !body.ReadUint8LengthPrefixed(&sessionID) {
		return "", false, nil
	}
	var cipherSuites cryptobyte.String
	if !body.ReadUint16LengthPrefixed(&cipherSuites) {
		return "", false, nil
	}
	var compressionMethods cryptobyte.String
	if !body.ReadUint8LengthPrefixed(&compressionMethods) {
		return "", false, nil
	}
	if body.Empty() {
		// No extensions block at all: a ClientHello with no server_name (RFC 8446
		// technically allows omitting extensions, though real clients always send
		// at least supported_versions in TLS 1.3, so this is truncation).
		return "", false, nil
	}
	var extensions cryptobyte.String
	if !body.ReadUint16LengthPrefixed(&extensions) {
		return "", false, nil
	}

	for !extensions.Empty() {
		var extType uint16
		var extData cryptobyte.String
		if !extensions.ReadUint16(&extType) || !extensions.ReadUint16LengthPrefixed(&extData) {
			return "", false, nil
		}
		if extType == tlsExtensionServerName {
			return parseServerNameExtension(extData)
		}
	}
	return "", false, nil
}

// parseServerNameExtension parses the body of a server_name extension (RFC 6066 §3) and
// returns the first host_name entry.
func parseServerNameExtension(extData cryptobyte.String) (string, bool, error) {
	var serverNameList cryptobyte.String
	if !extData.ReadUint16LengthPrefixed(&serverNameList) {
		return "", false, nil
	}
	for !serverNameList.Empty() {
		var nameType uint8
		if !serverNameList.ReadUint8(&nameType) {
			return "", false, nil
		}
		var name cryptobyte.String
		if !serverNameList.ReadUint16LengthPrefixed(&name) {
			return "", false, nil
		}
		if nameType == tlsServerNameTypeHostName {
			return string(name), true, nil
		}
	}
	return "", false, nil
}

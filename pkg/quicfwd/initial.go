package quicfwd

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/quic-go/quic-go/quicvarint"
	"golang.org/x/crypto/cryptobyte"
)

// initialSaltV1 is the salt HKDF-Extract uses, together with the client's chosen
// Destination Connection ID, to derive QUIC v1 Initial secrets (RFC 9001 §5.2). It is
// public, fixed by the RFC, and not a secret in any sense -- both ends of every QUIC v1
// connection derive from it -- which is exactly what lets a third party (this
// forwarder) read the ClientHello without holding any connection-specific key.
var initialSaltV1 = []byte{ //nolint:gochecknoglobals // constant
	0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
	0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a,
}

// ErrUnsupportedVersion is returned by ExtractSNI when the packet's QUIC version isn't
// one this package can derive Initial keys for. Per "The forwarder" in
// docs/plans/quic-transport/design.md, a version the forwarder cannot read Initial keys
// for is dropped silently, never routed by default -- this error is what a caller tests
// for to make that call.
var ErrUnsupportedVersion = errors.New("quicfwd: unsupported QUIC version")

// ErrNotInitial is returned by ExtractSNI when the packet is a long-header packet of a
// supported version but not an Initial packet.
var ErrNotInitial = errors.New("quicfwd: not an Initial packet")

// clientInitialKeys derives the AEAD key, AEAD IV, and header-protection key a QUIC v1
// client uses to protect Initial packets sent on the connection identified by dcid (the
// value of the Destination Connection ID field in the client's first Initial packet;
// RFC 9001 §5.2). All three are needed to remove header protection and then
// AEAD-decrypt an Initial packet's payload.
func clientInitialKeys(dcid []byte) (key, iv, hp []byte, err error) {
	initialSecret, err := hkdfExtract(dcid)
	if err != nil {
		return nil, nil, nil, err
	}
	clientSecret, err := hkdfExpandLabel(initialSecret, "client in", 32)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("quicfwd: derive client initial secret: %w", err)
	}
	if key, err = hkdfExpandLabel(clientSecret, "quic key", 16); err != nil {
		return nil, nil, nil, fmt.Errorf("quicfwd: derive client key: %w", err)
	}
	if iv, err = hkdfExpandLabel(clientSecret, "quic iv", 12); err != nil {
		return nil, nil, nil, fmt.Errorf("quicfwd: derive client iv: %w", err)
	}
	if hp, err = hkdfExpandLabel(clientSecret, "quic hp", 16); err != nil {
		return nil, nil, nil, fmt.Errorf("quicfwd: derive client hp: %w", err)
	}
	return key, iv, hp, nil
}

// hkdfExtract runs HKDF-Extract with the fixed v1 Initial salt and dcid as the input
// keying material, producing the intermediate secret RFC 9001 §5.2 derives both the
// client's and the server's Initial secrets from. This package only ever needs the
// client's side (ExtractSNI reads ClientHellos, never server responses).
func hkdfExtract(dcid []byte) ([]byte, error) {
	secret, err := hkdf.Extract(sha256.New, dcid, initialSaltV1)
	if err != nil {
		return nil, fmt.Errorf("quicfwd: derive initial secret: %w", err)
	}
	return secret, nil
}

// hkdfExpandLabel implements TLS 1.3's HKDF-Expand-Label (RFC 8446 §7.1), which RFC
// 9001 §5 reuses verbatim to turn a secret into the individual keys and IVs QUIC packet
// protection needs. The "tls13 " prefix on the label is part of the TLS 1.3
// specification, not something QUIC-specific. Every label RFC 9001 defines for Initial
// keys ("client in", "server in", "quic key", "quic iv", "quic hp") uses an empty
// Context, so this package has no need to take one as a parameter.
func hkdfExpandLabel(secret []byte, label string, length int) ([]byte, error) {
	var b cryptobyte.Builder
	b.AddUint16(uint16(length))
	b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddBytes([]byte("tls13 " + label))
	})
	b.AddUint8LengthPrefixed(func(*cryptobyte.Builder) {})
	info, err := b.Bytes()
	if err != nil {
		return nil, err
	}
	return hkdf.Expand(sha256.New, secret, string(info), length)
}

// removeHeaderProtectionAndDecrypt undoes RFC 9001 §5.4 header protection and then
// AEAD_AES_128_GCM-decrypts the payload of the long-header packet in b, which must
// already have been identified (via ParsePacket) as a QUIC v1 Initial packet. fields is
// the already-parsed version-independent prefix of the same packet.
func removeHeaderProtectionAndDecrypt(b []byte, fields longHeaderFields) ([]byte, error) {
	key, iv, hp, err := clientInitialKeys(fields.dcid)
	if err != nil {
		return nil, err
	}

	off := fields.off
	tokenLen, n, err := quicvarint.Parse(b[off:])
	if err != nil {
		return nil, fmt.Errorf("quicfwd: parse token length: %w", err)
	}
	off += n
	if off+int(tokenLen) > len(b) {
		return nil, fmt.Errorf("quicfwd: packet truncated within token")
	}
	off += int(tokenLen)

	length, n, err := quicvarint.Parse(b[off:])
	if err != nil {
		return nil, fmt.Errorf("quicfwd: parse length: %w", err)
	}
	off += n
	if off+int(length) > len(b) {
		return nil, fmt.Errorf("quicfwd: packet truncated within length-declared region")
	}
	pnOffset := off

	// Header protection sample: RFC 9001 §5.4.2 always assumes the maximum 4-byte
	// packet number encoding when locating the sample, skipping 4 bytes from the
	// start of the (still-protected) packet number field regardless of its actual
	// length -- which isn't known until the mask is applied and the low bits of
	// byte 0 are recovered.
	const sampleLen = 16
	sampleOffset := pnOffset + 4
	if sampleOffset+sampleLen > len(b) {
		return nil, fmt.Errorf("quicfwd: packet too short to sample for header protection")
	}
	sample := b[sampleOffset : sampleOffset+sampleLen]

	block, err := aes.NewCipher(hp)
	if err != nil {
		return nil, fmt.Errorf("quicfwd: header protection cipher: %w", err)
	}
	mask := make([]byte, block.BlockSize())
	block.Encrypt(mask, sample)

	// RFC 9001 §5.4.1: for a long header, only the low 4 bits of byte 0 (Reserved
	// + Packet Number Length) are protected.
	unprotectedFirst := b[0] ^ (mask[0] & 0x0f)
	pnLength := int(unprotectedFirst&0x03) + 1

	pnBytes := make([]byte, pnLength)
	for i := 0; i < pnLength; i++ {
		pnBytes[i] = b[pnOffset+i] ^ mask[1+i]
	}

	// RFC 9000 Appendix A packet-number decoding reconstructs the full packet
	// number from its truncated wire encoding relative to the largest packet
	// number processed so far. This forwarder tracks no per-connection state (by
	// design -- see "The forwarder" in docs/plans/quic-transport/design.md), so
	// there is no "largest so far"; treating it as -1 (no packet seen yet) is the
	// only value that makes sense, and with it the decoding algorithm always
	// yields the truncated value verbatim (the candidate reconstruction can only
	// move away from zero when the truncated value's top bit is set *and* that
	// makes it closer to a nonzero expected next value, which never happens when
	// the expected value is 0).
	packetNumber := uint64(0)
	for _, bb := range pnBytes {
		packetNumber = packetNumber<<8 | uint64(bb)
	}

	header := append([]byte(nil), b[:pnOffset+pnLength]...)
	header[0] = unprotectedFirst
	copy(header[pnOffset:pnOffset+pnLength], pnBytes)

	nonce := append([]byte(nil), iv...)
	var pnFull [8]byte
	binary.BigEndian.PutUint64(pnFull[:], packetNumber)
	for i := 0; i < 8; i++ {
		nonce[len(nonce)-8+i] ^= pnFull[i]
	}

	ciphertextEnd := pnOffset + int(length)
	if pnOffset+pnLength > ciphertextEnd {
		return nil, fmt.Errorf("quicfwd: packet number field longer than the declared length")
	}
	ciphertext := b[pnOffset+pnLength : ciphertextEnd]

	aeadBlock, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("quicfwd: AEAD cipher: %w", err)
	}
	aead, err := cipher.NewGCM(aeadBlock)
	if err != nil {
		return nil, fmt.Errorf("quicfwd: AEAD mode: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, header)
	if err != nil {
		return nil, fmt.Errorf("quicfwd: AEAD decrypt failed: %w", err)
	}
	return plaintext, nil
}

// cryptoSegment is one CRYPTO frame's contribution to the reassembled TLS handshake
// byte stream (RFC 9000 §19.6): its data starting at its own stream offset.
type cryptoSegment struct {
	offset uint64
	data   []byte
}

// frameReader consumes varints off the front of an Initial packet's decrypted payload,
// tracking the first error so a chain of parses can be written without an if-err-return
// after every field. Fields whose values this package doesn't need (ACK ranges, ECN
// counts, CONNECTION_CLOSE's error code and reason) are still parsed, never skipped by
// guessing their size, because a varint's own length isn't known without parsing it.
type frameReader struct {
	r   []byte
	err error
}

// varint consumes and returns one varint field, per quicvarint.Parse. If a previous call
// already failed, or this one does, the reader remembers the first error and returns 0
// for every call after that, so callers can chain several varint() calls and check err
// once at the end.
func (f *frameReader) varint(field string) uint64 {
	if f.err != nil {
		return 0
	}
	v, n, err := quicvarint.Parse(f.r)
	if err != nil {
		f.err = fmt.Errorf("quicfwd: parse %s: %w", field, err)
		return 0
	}
	f.r = f.r[n:]
	return v
}

// bytes consumes and returns the next n bytes, e.g. a CRYPTO or CONNECTION_CLOSE
// frame's variable-length content once its length has already been read with varint.
// The bounds check happens before any slicing, so a length longer than what remains
// is reported as a truncation error rather than panicking.
func (f *frameReader) bytes(n uint64, field string) []byte {
	if f.err != nil {
		return nil
	}
	if uint64(len(f.r)) < n {
		f.err = fmt.Errorf("quicfwd: %s truncated", field)
		return nil
	}
	b := f.r[:n]
	f.r = f.r[n:]
	return b
}

// walkInitialFrames parses the frames in an Initial packet's decrypted payload.
// RFC 9000 §12.4 restricts Initial packets to PADDING, PING, ACK, CRYPTO, and
// CONNECTION_CLOSE frames; any other frame type in this context is a protocol
// violation, so it is reported as an error rather than silently skipped.
func walkInitialFrames(payload []byte) ([]cryptoSegment, error) {
	var segments []cryptoSegment
	f := &frameReader{r: payload}
	for len(f.r) > 0 {
		typ := f.varint("frame type")
		if f.err != nil {
			return nil, f.err
		}
		switch typ {
		case 0x00: // PADDING (RFC 9000 §19.1): single byte, no content.
		case 0x01: // PING (RFC 9000 §19.2): single byte, no content.
		case 0x02, 0x03: // ACK / ACK_ECN (RFC 9000 §19.3).
			skipAckFrame(f, typ == 0x03)
		case 0x06: // CRYPTO (RFC 9000 §19.6).
			offset := f.varint("CRYPTO offset")
			length := f.varint("CRYPTO length")
			data := f.bytes(length, "CRYPTO frame")
			if f.err != nil {
				return nil, f.err
			}
			segments = append(segments, cryptoSegment{offset: offset, data: data})
		case 0x1c, 0x1d: // CONNECTION_CLOSE (RFC 9000 §19.19).
			skipConnectionCloseFrame(f, typ == 0x1c)
		default:
			return nil, fmt.Errorf("quicfwd: unexpected frame type 0x%x in Initial packet", typ)
		}
		if f.err != nil {
			return nil, f.err
		}
	}
	return segments, nil
}

// skipAckFrame consumes an ACK or ACK_ECN frame's fields after its type byte. withECN
// is true for ACK_ECN (type 0x03), which carries three additional ECN counts.
func skipAckFrame(f *frameReader, withECN bool) {
	f.varint("ACK largest acknowledged")
	f.varint("ACK delay")
	rangeCount := f.varint("ACK range count")
	f.varint("first ACK range")
	for i := uint64(0); f.err == nil && i < rangeCount; i++ {
		f.varint("ACK gap")
		f.varint("ACK range length")
	}
	if withECN {
		f.varint("ECT0 count")
		f.varint("ECT1 count")
		f.varint("ECN-CE count")
	}
}

// skipConnectionCloseFrame consumes a CONNECTION_CLOSE frame's fields after its type
// byte. withFrameType is true for the transport-level variant (type 0x1c), which
// carries an extra Frame Type field the application-level variant (0x1d) omits.
func skipConnectionCloseFrame(f *frameReader, withFrameType bool) {
	f.varint("CONNECTION_CLOSE error code")
	if withFrameType {
		f.varint("CONNECTION_CLOSE frame type")
	}
	reasonLen := f.varint("CONNECTION_CLOSE reason length")
	f.bytes(reasonLen, "CONNECTION_CLOSE reason")
}

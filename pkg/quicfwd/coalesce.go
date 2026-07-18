package quicfwd

import (
	"fmt"

	"github.com/quic-go/quic-go/quicvarint"
)

// SplitCoalesced splits a UDP datagram that may contain more than one QUIC packet (RFC
// 9000 §12.2 permits a sender to coalesce several packets, in order, into a single
// datagram, most commonly a client's Initial padded/followed by a 0-RTT packet, or a
// server's Initial followed by a Handshake packet) into its individual packets.
//
// Splitting relies on knowing each packet's own length, which -- unlike the DCID/SCID
// fields ParsePacket reads -- is not a version-independent invariant. Two things end a
// split and return the remaining bytes as one final element, because there is no
// generic way to locate a further boundary within them:
//   - A short-header packet carries no length field at all; the invariants (RFC 8999
//     §5.2) assume it always runs to the end of the datagram, which also means nothing
//     legitimately follows it, so it is always the last packet found.
//   - A long-header packet of a version this package doesn't have Initial keys for
//     (including the version-negotiation case, Version == 0), or a Version1 Retry
//     packet, which RFC 9000 §17.2.5 defines with no Length field, is never itself
//     coalesced with a following packet.
//
// A non-nil error means datagram doesn't parse as a sequence of QUIC packets at all
// (some prefix is truncated or otherwise malformed); the caller's response, per "The
// forwarder" in docs/reference/quic-transport-architecture.md, is the same silent drop as any
// other unparseable packet.
func SplitCoalesced(datagram []byte) ([][]byte, error) {
	var packets [][]byte
	b := datagram
	for len(b) > 0 {
		if b[0]&0x80 == 0 {
			// Short header: no length field, assumed to run to the end (and to
			// never be followed by anything else).
			packets = append(packets, b)
			break
		}

		fields, err := parseLongHeaderFields(b)
		if err != nil {
			return nil, err
		}
		if fields.version != Version1 {
			// A version this package can't interpret the type-specific layout
			// of (including version negotiation, version == 0): no way to
			// locate a further boundary, so the rest of the datagram is
			// reported as this one packet.
			packets = append(packets, b)
			break
		}

		typ := PacketType((b[0] >> 4) & 0x03)
		off := fields.off
		if typ == TypeInitial {
			// Only the Initial packet type carries a Token before its Length
			// (RFC 9000 §17.2.2).
			tokenLen, n, err := quicvarint.Parse(b[off:])
			if err != nil {
				return nil, fmt.Errorf("quicfwd: parse token length: %w", err)
			}
			off += n
			if off+int(tokenLen) > len(b) {
				return nil, fmt.Errorf("quicfwd: packet truncated within token")
			}
			off += int(tokenLen)
		}
		if typ == TypeRetry {
			// No Length field (RFC 9000 §17.2.5); a Retry is never coalesced
			// with a following packet.
			packets = append(packets, b)
			break
		}

		length, n, err := quicvarint.Parse(b[off:])
		if err != nil {
			return nil, fmt.Errorf("quicfwd: parse length: %w", err)
		}
		off += n
		pktLen := off + int(length)
		if pktLen > len(b) {
			return nil, fmt.Errorf("quicfwd: packet truncated within length-declared region")
		}
		packets = append(packets, b[:pktLen])
		b = b[pktLen:]
	}
	return packets, nil
}

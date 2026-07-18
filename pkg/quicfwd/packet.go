package quicfwd

import (
	"encoding/binary"
	"fmt"
)

// Wire version numbers this package knows about. Version1 is RFC 9000/9001; Version2 is
// RFC 9369, whose Initial salt, key-derivation labels, and long-header type bits all
// differ from v1's. Only Version1 Initial decryption is implemented (see ExtractSNI);
// Version2 is recognized here only so ParsePacket can report it accurately rather than
// misreading it as v1.
const (
	Version1 uint32 = 0x00000001
	Version2 uint32 = 0x6b3343cf
)

// Kind classifies a QUIC packet as far as the RFC 8999 invariants allow without
// knowing the version.
type Kind int

const (
	// KindShortHeader is a 1-RTT packet: fixed-length DCID, no version, no SCID.
	KindShortHeader Kind = iota
	// KindLongHeader is any long-header packet of a recognized (non-zero) version:
	// Initial, 0-RTT, Handshake, or Retry.
	KindLongHeader
	// KindVersionNegotiation is a long-header packet with Version == 0 (RFC 8999
	// §6). It carries no CRYPTO data and is never produced by a Telepresence
	// client; it exists in the type space so a forwarder can recognize and drop
	// it rather than misreading its version-less body as a v1 packet.
	KindVersionNegotiation
)

func (k Kind) String() string {
	switch k {
	case KindShortHeader:
		return "short-header"
	case KindLongHeader:
		return "long-header"
	case KindVersionNegotiation:
		return "version-negotiation"
	default:
		return fmt.Sprintf("Kind(%d)", int(k))
	}
}

// PacketType is the Long Packet Type carried in the low two bits of byte 0 of a long
// header (RFC 9000 §17.2). Those bits are sent in the clear -- header protection
// (RFC 9001 §5.4.1) only ever covers the Reserved and Packet Number Length bits below
// them -- so they can be read before removing any protection, but their *meaning* is
// version-specific: RFC 9369 §3.2 permutes the four values for QUIC v2. TypeUnknown is
// returned whenever the packet's version isn't one this package assigns meaning to.
type PacketType int8

const (
	TypeUnknown PacketType = iota - 1
	TypeInitial
	TypeZeroRTT
	TypeHandshake
	TypeRetry
)

func (t PacketType) String() string {
	switch t {
	case TypeInitial:
		return "Initial"
	case TypeZeroRTT:
		return "0-RTT"
	case TypeHandshake:
		return "Handshake"
	case TypeRetry:
		return "Retry"
	default:
		return "unknown"
	}
}

// PacketInfo is the result of classifying a single QUIC packet enough to route it, per
// the QUIC-LB pattern described in "The forwarder" section of
// docs/reference/quic-transport-architecture.md: SNI for a connection's first packet, CID for
// every packet after that.
type PacketInfo struct {
	Kind Kind

	// Version is the wire version field of a long-header packet. Populated only
	// when Kind != KindShortHeader (short headers carry no version).
	Version uint32

	// Type is the long-header packet type, valid only when Kind == KindLongHeader
	// and Version == Version1 (see PacketType). It is TypeUnknown otherwise --
	// including, deliberately, for every version this package doesn't have Initial
	// key derivation for, so a caller cannot accidentally treat an unfamiliar
	// version's type bits as if they meant what they mean in v1.
	Type PacketType

	// DCID is the Destination Connection ID: for a short header, the fixed
	// CIDLen-byte field this package's CID format requires; for a long header, the
	// variable-length field the invariants guarantee regardless of version.
	DCID []byte

	// SCID is the Source Connection ID. Populated only for long-header packets.
	SCID []byte
}

// ParsePacket classifies a single QUIC packet far enough to route it: long vs. short
// header, and the fields each carries per the RFC 8999 invariants. It never needs to
// understand a version to do this -- that is the entire point of the invariants -- so
// an unrecognized Version is not an error here; it is reported via PacketInfo.Version
// (and, for long headers, PacketInfo.Type staying TypeUnknown) for the caller to act on.
// ParsePacket only fails when b is too short to contain the fields it claims to.
func ParsePacket(b []byte) (PacketInfo, error) {
	if len(b) < 1 {
		return PacketInfo{}, fmt.Errorf("quicfwd: empty packet")
	}
	first := b[0]
	if first&0x80 == 0 {
		// Short header (RFC 9000 §17.3.1): 1-byte header followed immediately by
		// the DCID. Its length isn't in the packet -- the endpoint that assigned
		// it is assumed to know -- so we require our own fixed CIDLen.
		if len(b) < 1+CIDLen {
			return PacketInfo{}, fmt.Errorf("quicfwd: short header packet too small for a %d-byte connection ID", CIDLen)
		}
		dcid := append([]byte(nil), b[1:1+CIDLen]...)
		return PacketInfo{Kind: KindShortHeader, DCID: dcid}, nil
	}

	fields, err := parseLongHeaderFields(b)
	if err != nil {
		return PacketInfo{}, err
	}
	kind := KindLongHeader
	typ := TypeUnknown
	switch fields.version {
	case 0:
		kind = KindVersionNegotiation
	case Version1:
		typ = PacketType((first >> 4) & 0x03)
	}
	return PacketInfo{
		Kind:    kind,
		Version: fields.version,
		Type:    typ,
		DCID:    fields.dcid,
		SCID:    fields.scid,
	}, nil
}

// longHeaderFields holds the version-independent long-header fields (RFC 8999 §5.1)
// plus off, the offset of the first byte after the Source Connection ID -- where every
// version-specific field (token, length, packet number, payload) begins.
type longHeaderFields struct {
	version uint32
	dcid    []byte
	scid    []byte
	off     int
}

// parseLongHeaderFields reads the version, DCID, and SCID of a long-header packet. This
// layout is a QUIC invariant (RFC 8999 §5.1): it holds for every version, including
// versions this package otherwise knows nothing about, which is what lets a
// version-oblivious router extract routing information from any QUIC long-header
// packet at all.
func parseLongHeaderFields(b []byte) (longHeaderFields, error) {
	if len(b) < 5 {
		return longHeaderFields{}, fmt.Errorf("quicfwd: long header packet too small for a version field")
	}
	version := binary.BigEndian.Uint32(b[1:5])
	off := 5

	if off >= len(b) {
		return longHeaderFields{}, fmt.Errorf("quicfwd: long header packet truncated before DCID length")
	}
	dcil := int(b[off])
	off++
	if off+dcil > len(b) {
		return longHeaderFields{}, fmt.Errorf("quicfwd: long header packet truncated within DCID")
	}
	dcid := append([]byte(nil), b[off:off+dcil]...)
	off += dcil

	if off >= len(b) {
		return longHeaderFields{}, fmt.Errorf("quicfwd: long header packet truncated before SCID length")
	}
	scil := int(b[off])
	off++
	if off+scil > len(b) {
		return longHeaderFields{}, fmt.Errorf("quicfwd: long header packet truncated within SCID")
	}
	scid := append([]byte(nil), b[off:off+scil]...)
	off += scil

	return longHeaderFields{version: version, dcid: dcid, scid: scid, off: off}, nil
}

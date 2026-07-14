package quicfwd

import (
	"crypto/rand"
	"fmt"
	"net/netip"

	"github.com/quic-go/quic-go"
)

// CIDLen is the fixed length, in bytes, of every connection ID this package mints or
// decodes. RFC 9000 §17.2 caps connection IDs at 20 bytes; using exactly that maximum
// for every server-issued CID means the length alone identifies "one of ours" before a
// single byte is inspected, and leaves no shorter length available for a forwarder or
// backend to mistake for a foreign/legacy format.
const CIDLen = 20

// cidMagicV1 occupies byte 0 of every CID this package encodes. Its purpose is purely
// discriminative: a forwarder or backend that receives an arbitrary 20-byte connection
// ID (its own past output, another implementation's random CID, or attacker-forged
// bytes) can reject anything that doesn't start with this value cheaply, before
// attempting to interpret the rest as an encoded pod IP. A future format revision would
// pick a different constant here and DecodeCID would grow a case for it; old and new
// formats coexist because the magic byte, not the CID length, selects the layout.
const cidMagicV1 = 0xC5

// IP family flags, stored in byte 1 of the CID.
const (
	familyIPv4 = 0
	familyIPv6 = 1
)

// Layout of an encoded CID (RFC 9000 invariant: opaque to everyone but the minter):
//
//	byte 0      cidMagicV1
//	byte 1      family (familyIPv4 | familyIPv6)
//	bytes 2..N  the IP address, 4 bytes for IPv4 or 16 for IPv6
//	bytes N..20 random, for uniqueness (RFC 9000 §5.1.1 requires CIDs be
//	            hard to correlate; the IP prefix is deliberately visible per
//	            the design's decision to defer encrypted CIDs, but the tail
//	            must still differ connection to connection)
const (
	cidHeaderIPv4 = 2 + 4  // magic + family + 4-byte IPv4 address
	cidHeaderIPv6 = 2 + 16 // magic + family + 16-byte IPv6 address
)

// EncodeCID returns a new 20-byte QUIC connection ID that encodes ip. Backends
// (traffic-manager, traffic-agents) call this from their ConnectionIDGenerator so that
// every server-issued CID lets the forwarder route mid-connection packets straight to
// the pod that owns the connection, without any per-connection state (see "The
// forwarder" in docs/reference/quic-transport-architecture.md).
func EncodeCID(ip netip.Addr) ([]byte, error) {
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return nil, fmt.Errorf("quicfwd: cannot encode CID for address %s", ip)
	}
	buf := make([]byte, CIDLen)
	buf[0] = cidMagicV1
	var headerLen int
	switch {
	case ip.Is4() || ip.Is4In6():
		buf[1] = familyIPv4
		a4 := ip.As4()
		copy(buf[2:], a4[:])
		headerLen = cidHeaderIPv4
	default:
		buf[1] = familyIPv6
		a16 := ip.As16()
		copy(buf[2:], a16[:])
		headerLen = cidHeaderIPv6
	}
	if _, err := rand.Read(buf[headerLen:]); err != nil {
		return nil, fmt.Errorf("quicfwd: fill random CID tail: %w", err)
	}
	return buf, nil
}

// DecodeCID extracts the pod IP encoded in cid by EncodeCID. It returns ok == false,
// rather than an error, for anything that isn't a recognizable CID of this format: the
// wrong length, an unrecognized magic byte, an unrecognized family, or an
// unspecified/multicast address. None of these can be a genuine backend-minted CID,
// so the caller's correct response in every case is the same -- treat the packet as
// unroutable -- and a bool return keeps that call site a single "if !ok" check rather
// than an error type switch.
func DecodeCID(cid []byte) (netip.Addr, bool) {
	if len(cid) != CIDLen || cid[0] != cidMagicV1 {
		return netip.Addr{}, false
	}
	var ip netip.Addr
	switch cid[1] {
	case familyIPv4:
		ip = netip.AddrFrom4([4]byte(cid[2:cidHeaderIPv4]))
	case familyIPv6:
		ip = netip.AddrFrom16([16]byte(cid[2:cidHeaderIPv6]))
	default:
		return netip.Addr{}, false
	}
	if ip.IsUnspecified() || ip.IsMulticast() {
		return netip.Addr{}, false
	}
	return ip, true
}

// CIDGenerator implements quic-go's quic.ConnectionIDGenerator, minting CIDs that all
// encode the same pod IP. A backend (traffic-manager or traffic-agent) configures one
// of these, built from its own pod IP, on the quic.Transport it listens with; every
// connection ID it then hands out to clients -- for every connection, not just the
// first -- decodes back to that pod via DecodeCID, which is what lets the forwarder
// route established connections without keeping a flow table.
type CIDGenerator struct {
	ip netip.Addr
}

// NewCIDGenerator returns a CIDGenerator that mints connection IDs encoding ip.
func NewCIDGenerator(ip netip.Addr) *CIDGenerator {
	return &CIDGenerator{ip: ip}
}

// GenerateConnectionID implements quic.ConnectionIDGenerator.
func (g *CIDGenerator) GenerateConnectionID() (quic.ConnectionID, error) {
	b, err := EncodeCID(g.ip)
	if err != nil {
		return quic.ConnectionID{}, err
	}
	return quic.ConnectionIDFromBytes(b), nil
}

// ConnectionIDLen implements quic.ConnectionIDGenerator. Every CID minted by this
// generator has the same fixed length, as quic-go requires.
func (g *CIDGenerator) ConnectionIDLen() int {
	return CIDLen
}

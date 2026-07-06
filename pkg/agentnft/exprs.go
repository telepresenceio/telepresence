//go:build linux

package agentnft

import (
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// Every helper below is a self-contained matcher or action, expressed with
// nftables registers. Matches that don't need to stay alive alongside another
// value (load, compare, discard) reuse register 1; the only places a second
// live value is needed are dnatTo (address in reg 1, port in reg 2) and
// redirectViaProtoPortMap (the two concatenated lookup-key components), which
// mirrors how the nft compiler allocates registers for an equivalent rule
// (see google/nftables' own TestConfigureNAT).

// matchL4Proto returns expressions that match the transport protocol
// (`meta l4proto <p>`). types.Proto is itself the IP protocol number, so its
// byte value is exactly what l4proto compares against.
func matchL4Proto(p types.Proto) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{byte(p)}},
	}
}

// matchOif returns expressions that match the outbound interface name
// (`oifname "name"`).
func matchOif(name string) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifnameBytes(name)},
	}
}

// matchOwner returns expressions that match (or, when neq is true, exclude)
// the traffic-agent's own sockets. Normally this is a socket-ownership match,
// via `meta skgid`/`meta skuid`, keyed on the agent's distinct group or user
// ID. When owner.Mark is non-zero, it instead matches the packet's firewall
// mark (`meta mark`): a socket-owner match cannot be installed into a network
// namespace owned by a non-init user namespace, so a target in that situation
// is told apart by mark instead.
func matchOwner(owner OwnerMatch, neq bool) []expr.Any {
	op := expr.CmpOpEq
	if neq {
		op = expr.CmpOpNeq
	}
	if owner.Mark != 0 {
		return []expr.Any{
			&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
			// meta mark, like skgid/skuid, is a host-endian datatype: nft does
			// not byte-swap it the way it does for network fields such as ports
			// or addresses. Comparing with the wrong byte order silently never
			// matches.
			&expr.Cmp{Op: op, Register: 1, Data: binaryutil.NativeEndian.PutUint32(owner.Mark)},
		}
	}
	key := expr.MetaKeySKGID
	if !owner.UseGID {
		key = expr.MetaKeySKUID
	}
	return []expr.Any{
		&expr.Meta{Key: key, Register: 1},
		// skgid/skuid, like meta mark, are host-endian datatypes: nft does not
		// byte-swap them the way it does for network fields such as ports or
		// addresses. Comparing with the wrong byte order silently never matches.
		&expr.Cmp{Op: op, Register: 1, Data: binaryutil.NativeEndian.PutUint32(owner.ID)},
	}
}

// matchDport returns expressions that match (or, when neq is true, exclude)
// the transport-header destination port.
func matchDport(port uint16, neq bool) []expr.Any {
	op := expr.CmpOpEq
	if neq {
		op = expr.CmpOpNeq
	}
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Cmp{Op: op, Register: 1, Data: binaryutil.BigEndian.PutUint16(port)},
	}
}

// matchDaddr returns expressions that match (or, when neq is true, exclude)
// the network-header destination address against addr.
func matchDaddr(off, ln uint32, addr netip.Addr, neq bool) []expr.Any {
	op := expr.CmpOpEq
	if neq {
		op = expr.CmpOpNeq
	}
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: off, Len: ln},
		&expr.Cmp{Op: op, Register: 1, Data: addr.AsSlice()},
	}
}

// matchDaddrNotInSet returns expressions that only match destinations NOT in m
// -- the nft equivalent of `ip daddr != @set`.
func matchDaddrNotInSet(off, ln uint32, m *nftables.Set) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: off, Len: ln},
		&expr.Lookup{SourceRegister: 1, SetName: m.Name, SetID: m.ID, Invert: true},
	}
}

// redirectViaProtoPortMap returns expressions that look the packet's
// (l4proto . dport) pair up in m (container-port -> agent-port) and, when
// found, redirect the packet to that port on the local address
// (`meta l4proto . th dport map { ... } redirect`). A packet whose protocol
// and port pair is not a key in m simply doesn't match and rule evaluation
// falls through. The two key components are loaded into consecutive 4-byte
// registers: reg 1 is NFT_REG_1, whose first 32-bit word is NFT_REG32_00, so
// the second component goes into NFT_REG32_01; short loads zero the rest of
// their word, matching the 4-byte padding of the map's element keys.
//
// There is no verdict-map equivalent: verdict maps can only carry fixed
// actions (accept/drop/queue/jump/goto/return), never a computed redirect
// port, so a regular map plus an explicit "redir" statement referencing the
// register the lookup wrote into is the only way to express it.
func redirectViaProtoPortMap(m *nftables.Set) []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Payload{DestRegister: unix.NFT_REG32_01, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
		&expr.Lookup{SourceRegister: 1, DestRegister: 1, IsDestRegSet: true, SetName: m.Name, SetID: m.ID},
		&expr.Redir{RegisterProtoMin: 1},
	}
}

// dnatTo returns expressions that DNAT the packet to addr:port. Used for the
// proxy-port -> container-port rewrite of numeric-target intercepts.
func dnatTo(family nftables.TableFamily, addr netip.Addr, port uint16) []expr.Any {
	return []expr.Any{
		&expr.Immediate{Register: 1, Data: addr.AsSlice()},
		&expr.Immediate{Register: 2, Data: binaryutil.BigEndian.PutUint16(port)},
		&expr.NAT{Type: expr.NATTypeDestNAT, Family: uint32(family), RegAddrMin: 1, RegProtoMin: 2},
	}
}

// identityDNAT returns expressions for `dnat to ip[6] daddr`: an identity DNAT
// that maps the connection to the destination it already has. It changes
// nothing about the packet, but it pins the connection's NAT decision in
// conntrack, so a mesh's later (lower-priority) outbound redirect becomes a
// no-op and the packet reaches its original destination directly. This is how
// the agent's own egress bypasses the mesh without sharing a chain with it.
func identityDNAT(family nftables.TableFamily, off, ln uint32) []expr.Any {
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: off, Len: ln},
		&expr.NAT{Type: expr.NATTypeDestNAT, Family: uint32(family), RegAddrMin: 1},
	}
}

// ifnameBytes pads name to IFNAMSIZ bytes the way nft encodes interface-name
// comparisons.
func ifnameBytes(name string) []byte {
	b := make([]byte, ifNameSize)
	copy(b, name)
	return b
}

package vif

import (
	"context"
	"net/netip"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// TelepresenceULA6 is the IPv6 unique-local prefix that the device draws its owned
// IPv6 source address from, and that the session draws IPv6 virtual IPs from. There
// is no configurable IPv6 virtual subnet, so this fixed ULA plays the same role the
// IPv4 virtual subnet does: a Telepresence-owned range whose addresses never collide
// with a cluster address. Owned addresses count down from the top of the range while
// virtual IPs count up from the bottom, so the two uses never overlap.
var TelepresenceULA6 = netip.MustParsePrefix("fd00:0:0:246::/64") //nolint:gochecknoglobals // constant

// ownedAddress returns the single source address the device claims when routing
// subnets of the given family. The device is a router toward the cluster, not a
// member of any cluster subnet, so it owns exactly one address per family — drawn
// from a Telepresence-owned range (the configured virtual subnet for IPv4, a fixed
// ULA for IPv6) and offset down from the top of that range by the interface index.
// The offset makes the address unique across every concurrent device on the host,
// including those owned by separate telepresence processes. The second return value
// is false when the range is too small to carry an address at the given offset.
func ownedAddress(ctx context.Context, ipv4 bool, ifaceIndex uint32) (netip.Addr, bool) {
	rng := TelepresenceULA6
	if ipv4 {
		rng = client.GetConfig(ctx).Routing().VirtualSubnet
	}
	if !rng.IsValid() || rng.Addr().Is4() != ipv4 {
		return netip.Addr{}, false
	}
	hostBits := rng.Addr().BitLen() - rng.Bits()
	if hostBits < 2 || uint64(ifaceIndex)+2 >= uint64(1)<<min(hostBits, 62) {
		return netip.Addr{}, false
	}
	// Count down from the top of the range (the last host address below the broadcast
	// address), leaving the network and broadcast addresses untouched.
	owned := addrMinus(broadcastAddr(rng), uint64(ifaceIndex)+1)
	// Owned addresses occupy the upper half of the range; the lower half is reserved
	// for virtual IPs, which count up from the bottom (see pkg/client/rootd/vip
	// NewGenerator). Reject an offset that would fall into the VIP half so a virtual
	// IP and an owned address can never collide.
	if rng.Bits() < rng.Addr().BitLen() {
		if lowerHalf := netip.PrefixFrom(rng.Masked().Addr(), rng.Bits()+1); lowerHalf.Contains(owned) {
			return netip.Addr{}, false
		}
	}
	return owned, true
}

// broadcastAddr returns the all-host-bits-set address of the prefix (its IPv4
// broadcast address, or the IPv6 equivalent).
func broadcastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	hostBits := len(b)*8 - p.Bits()
	for i := len(b) - 1; hostBits > 0 && i >= 0; i-- {
		n := min(hostBits, 8)
		b[i] |= byte(0xff) >> (8 - n)
		hostBits -= n
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// addrMinus subtracts n from the given address, treating it as a big-endian integer.
func addrMinus(a netip.Addr, n uint64) netip.Addr {
	b := a.AsSlice()
	for i := len(b) - 1; i >= 0 && n > 0; i-- {
		d := byte(n)
		if b[i] >= d {
			b[i] -= d
			n >>= 8
		} else {
			b[i] = byte(uint64(b[i]) + 0x100 - uint64(d))
			n = (n >> 8) + 1
		}
	}
	r, _ := netip.AddrFromSlice(b)
	return r
}

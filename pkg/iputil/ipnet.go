package iputil

import (
	"net"
	"net/netip"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func PrefixToRPC(n netip.Prefix) *manager.IPNet {
	return &manager.IPNet{
		Ip:   n.Addr().AsSlice(),
		Mask: int32(n.Bits()),
	}
}

func PrefixesToRPC(n []netip.Prefix) []*manager.IPNet {
	l := len(n)
	if l == 0 {
		return nil
	}
	ss := make([]*manager.IPNet, l)
	for i, m := range n {
		ss[i] = PrefixToRPC(m)
	}
	return ss
}

func RPCToPrefix(m *manager.IPNet) netip.Prefix {
	if a, ok := netip.AddrFromSlice(m.Ip); ok {
		return netip.PrefixFrom(a, int(m.Mask))
	}
	return netip.Prefix{}
}

func RPCsToPrefixes(n []*manager.IPNet) []netip.Prefix {
	l := len(n)
	if l == 0 {
		return nil
	}
	ss := make([]netip.Prefix, l)
	for i, m := range n {
		ss[i] = RPCToPrefix(m)
	}
	return ss
}

func PrefixFromIPNet(ipNet *net.IPNet) (pfx netip.Prefix) {
	if ipNet == nil {
		return pfx
	}
	if addr, ok := netip.AddrFromSlice(ipNet.IP); ok {
		if addr.Is4In6() {
			addr = netip.AddrFrom4(addr.As4())
		}
		ones, _ := ipNet.Mask.Size()
		pfx = netip.PrefixFrom(addr, ones)
	}
	return pfx
}

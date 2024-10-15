package iputil

import (
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

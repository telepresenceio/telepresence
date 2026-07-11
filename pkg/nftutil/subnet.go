//go:build linux

package nftutil

import (
	"fmt"
	"net/netip"
	"sort"

	"github.com/google/nftables"
)

// IntervalElements converts subnets into the element pairs an nftables
// interval set expects: one element at the network address (the start of the
// range) and, unless the range extends to the top of the address space, one
// at the address immediately following the last address in the range,
// flagged as the interval's end (SetElement.IntervalEnd). Overlapping or
// adjacent subnets are left to the kernel's auto-merge (Set.AutoMerge) to
// coalesce; this function only needs to hand it correctly ordered boundaries.
func IntervalElements(prefixes []netip.Prefix) ([]nftables.SetElement, error) {
	type bound struct {
		addr netip.Addr
		end  bool
	}
	bounds := make([]bound, 0, len(prefixes)*2)
	for _, p := range prefixes {
		if !p.IsValid() {
			return nil, fmt.Errorf("nftutil: invalid subnet %s", p)
		}
		bounds = append(bounds, bound{addr: p.Masked().Addr()})
		if end := lastAddr(p).Next(); end.IsValid() {
			bounds = append(bounds, bound{addr: end, end: true})
		}
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i].addr.Less(bounds[j].addr) })

	elems := make([]nftables.SetElement, len(bounds))
	for i, b := range bounds {
		elems[i] = nftables.SetElement{Key: b.addr.AsSlice(), IntervalEnd: b.end}
	}
	return elems, nil
}

// lastAddr returns the last (highest) address contained in p, i.e. p's
// network address with every host bit set to 1.
func lastAddr(p netip.Prefix) netip.Addr {
	p = p.Masked()
	b := p.Addr().AsSlice()
	bits := p.Bits()
	for i := range b {
		byteStart := i * 8
		switch {
		case byteStart+8 <= bits:
			// fully within the prefix; keep the network bits as-is.
		case byteStart >= bits:
			b[i] = 0xFF
		default:
			free := 8 - (bits - byteStart)
			b[i] |= byte(1<<free) - 1
		}
	}
	addr, _ := netip.AddrFromSlice(b)
	return addr
}

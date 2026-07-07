//go:build linux

package nftutil

import (
	"fmt"
	"net/netip"
	"sort"

	"github.com/google/nftables"
)

// IntervalElements converts subnets into the element pairs an nftables
// interval set expects: for each maximal contiguous range, one element at the
// range's first address and, unless the range reaches the top of the address
// space, one at the address immediately following its last address, flagged as
// the interval's end (SetElement.IntervalEnd). Overlapping or adjacent subnets
// are coalesced into a single range first, because the kernel rejects a batch
// that inserts overlapping intervals -- Set.AutoMerge only records a userspace
// hint (NFTNL_UDATA_SET_MERGE_ELEMENTS) and performs no merging itself.
func IntervalElements(prefixes []netip.Prefix) ([]nftables.SetElement, error) {
	type rng struct{ start, end netip.Addr }
	ranges := make([]rng, 0, len(prefixes))
	for _, p := range prefixes {
		if !p.IsValid() {
			return nil, fmt.Errorf("nftutil: invalid subnet %s", p)
		}
		ranges = append(ranges, rng{start: p.Masked().Addr(), end: lastAddr(p)})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].start.Less(ranges[j].start) })

	// Coalesce overlapping and adjacent ranges. The input is sorted by start, so
	// a range merges into the previous one when it begins at or before the
	// previous range's end, or immediately after it. Different address families
	// never merge: an address of another family never falls at or before
	// cur.end.Next().
	merged := make([]rng, 0, len(ranges))
	for _, r := range ranges {
		if n := len(merged); n > 0 {
			cur := &merged[n-1]
			if !cur.end.Less(r.start) || cur.end.Next() == r.start {
				if cur.end.Less(r.end) {
					cur.end = r.end
				}
				continue
			}
		}
		merged = append(merged, r)
	}

	elems := make([]nftables.SetElement, 0, len(merged)*2)
	for _, r := range merged {
		elems = append(elems, nftables.SetElement{Key: r.start.AsSlice()})
		if end := r.end.Next(); end.IsValid() {
			elems = append(elems, nftables.SetElement{Key: end.AsSlice(), IntervalEnd: true})
		}
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

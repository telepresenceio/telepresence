//go:build linux

package nftutil

import (
	"net/netip"

	"github.com/google/nftables"
)

// AddrLayout returns the network-header offset and length, in bytes, of the
// destination address field for family.
func AddrLayout(family nftables.TableFamily) (offset, length uint32) {
	if family == nftables.TableFamilyIPv6 {
		return 24, 16
	}
	return 16, 4
}

// IntervalSet builds an interval set of prefixes: named name in table, with a
// key type chosen from table.Family (ipv6_addr for TableFamilyIPv6, ipv4_addr
// otherwise). Overlapping and adjacent prefixes are coalesced by
// IntervalElements before they reach the kernel, which rejects a batch that
// inserts overlapping intervals.
func IntervalSet(table *nftables.Table, name string, prefixes []netip.Prefix) (*SetData, error) {
	elems, err := IntervalElements(prefixes)
	if err != nil {
		return nil, err
	}
	keyType := nftables.TypeIPAddr
	if table.Family == nftables.TableFamilyIPv6 {
		keyType = nftables.TypeIP6Addr
	}
	return &SetData{
		Set: &nftables.Set{
			Table:    table,
			Name:     name,
			KeyType:  keyType,
			Interval: true,
		},
		Elements: elems,
	}, nil
}

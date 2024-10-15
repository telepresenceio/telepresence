package subnet

import (
	"net/netip"
	"sort"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

// Set represents a unique unordered set of subnets.
type Set map[netip.Prefix]struct{}

func NewSet(subnets []netip.Prefix) Set {
	s := make(Set, len(subnets))
	for _, subnet := range subnets {
		s.Add(subnet)
	}
	return s
}

// Equals returns true if the two sets have the same content.
func (s Set) Equals(o Set) bool {
	if len(s) != len(o) {
		return false
	}
	for key := range s {
		if _, ok := o[key]; !ok {
			return false
		}
	}
	return true
}

// AppendSortedTo appends the sorted subnets of this set to the given slice and returns
// the resulting slice.
func (s Set) AppendSortedTo(subnets []netip.Prefix) []netip.Prefix {
	sz := len(s)
	if sz == 0 {
		return subnets
	}
	// Ensure capacity of the slice
	need := len(subnets) + sz
	if cap(subnets) < need {
		ns := make([]netip.Prefix, len(subnets), need)
		copy(ns, subnets)
		subnets = ns
	}
	return append(subnets, s.sortedKeys()...)
}

// Add adds a subnet to this set unless it doesn't already exist. Returns true if the subnet was added, false otherwise.
func (s Set) Add(subnet netip.Prefix) bool {
	if _, ok := s[subnet]; ok {
		return false
	}
	s[subnet] = struct{}{}
	return true
}

// Delete deletes a subnet equal to the given subnet. Returns true if the subnet was deleted, false otherwise.
func (s Set) Delete(subnet netip.Prefix) bool {
	if _, ok := s[subnet]; ok {
		delete(s, subnet)
		return true
	}
	return false
}

// Clone returns a copy of this Set.
func (s Set) Clone() Set {
	return maps.Copy(s)
}

func (s Set) String() string {
	if s == nil {
		return "nil"
	}
	sb := strings.Builder{}
	sb.WriteByte('[')
	for i, key := range s.sortedKeys() {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(key.String())
	}
	sb.WriteByte(']')
	return sb.String()
}

func (s Set) sortedKeys() []netip.Prefix {
	ks := make([]netip.Prefix, len(s))
	i := 0
	for k := range s {
		ks[i] = k
		i++
	}
	sort.Slice(ks, func(i, j int) bool {
		ia := ks[i].Addr()
		ja := ks[j].Addr()
		if ia == ja {
			return ks[i].Bits() < ks[j].Bits()
		}
		return ia.Less(ja)
	})
	return ks
}

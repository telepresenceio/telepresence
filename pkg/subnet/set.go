package subnet

import (
	"bytes"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

type setKey struct {
	iputil.IPKey
	int
}

func newSetKey(subnet *net.IPNet) setKey {
	ones, _ := subnet.Mask.Size()
	return setKey{iputil.IPKey(subnet.IP), ones}
}

func (sk setKey) compare(o setKey) int {
	if cmp := bytes.Compare(sk.IPKey.IP(), o.IPKey.IP()); cmp != 0 {
		return cmp
	}
	return sk.int - o.int
}

func (sk setKey) toSubnet() *net.IPNet {
	ip := sk.IP()
	return &net.IPNet{
		IP:   ip,
		Mask: net.CIDRMask(sk.int, len(ip)*8),
	}
}

// Set represents a unique unordered set of subnets.
type Set map[setKey]struct{}

func NewSet(subnets []*net.IPNet) Set {
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
func (s Set) AppendSortedTo(subnets []*net.IPNet) []*net.IPNet {
	sz := len(s)
	if sz == 0 {
		return subnets
	}
	// Ensure capacity of the slice
	need := len(subnets) + sz
	if cap(subnets) < need {
		ns := make([]*net.IPNet, len(subnets), need)
		copy(ns, subnets)
		subnets = ns
	}

	for _, key := range s.sortedKeys() {
		subnets = append(subnets, key.toSubnet())
	}
	return subnets
}

// Add adds a subnet to this set unless it doesn't already exist. Returns true if the subnet was added, false otherwise.
func (s Set) Add(subnet *net.IPNet) bool {
	key := newSetKey(subnet)
	if _, ok := s[key]; ok {
		return false
	}
	s[key] = struct{}{}
	return true
}

// Delete deletes a subnet equal to the given subnet. Returns true if the subnet was deleted, false otherwise.
func (s Set) Delete(subnet *net.IPNet) bool {
	key := newSetKey(subnet)
	if _, ok := s[key]; ok {
		delete(s, key)
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
		sb.WriteString(key.IP().String())
		sb.WriteByte('/')
		sb.WriteString(strconv.Itoa(key.int))
	}
	sb.WriteByte(']')
	return sb.String()
}

func (s Set) sortedKeys() []setKey {
	ks := make([]setKey, len(s))
	i := 0
	for k := range s {
		ks[i] = k
		i++
	}
	sort.Slice(ks, func(i, j int) bool {
		return ks[i].compare(ks[j]) < 0
	})
	return ks
}

package vip

import (
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
)

type Generator interface {
	Next() (netip.Addr, error)
	Subnet() netip.Prefix
}

// Generators allocates virtual IPs from a separate subnet per address family. A
// cluster address is always mapped to a virtual IP of its own family, so an IPv4
// destination draws from the IPv4 virtual subnet and an IPv6 destination from the
// IPv6 range. A single IPv4 generator can never represent an IPv6 address, so the
// two families must be kept apart.
type Generators struct {
	mu                 sync.Mutex
	v4, v6             Generator
	v4Subnet, v6Subnet netip.Prefix
}

// NewGenerators returns generators that draw IPv4 virtual IPs from v4Subnet and
// IPv6 virtual IPs from v6Subnet. The actual per-family generator is created
// lazily by EnsureFamily, since the families that will be translated are not
// always known when the proxy-via workloads are activated.
func NewGenerators(v4Subnet, v6Subnet netip.Prefix) *Generators {
	return &Generators{v4Subnet: v4Subnet, v6Subnet: v6Subnet}
}

// EnsureFamily makes sure a generator exists for the family of addr, creating it
// from the configured subnet for that family. An existing generator is never
// replaced, so virtual IPs already handed out remain valid across repeated calls.
func (g *Generators) EnsureFamily(addr netip.Addr) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case addr.Is4():
		if g.v4 == nil && g.v4Subnet.IsValid() {
			g.v4 = NewGenerator(g.v4Subnet)
		}
	default:
		if g.v6 == nil && g.v6Subnet.IsValid() {
			g.v6 = NewGenerator(g.v6Subnet)
		}
	}
}

// Next returns the next virtual IP from the generator matching forAddr's family.
// It returns an error if no generator has been created for that family.
func (g *Generators) Next(forAddr netip.Addr) (netip.Addr, error) {
	g.mu.Lock()
	gen := g.v6
	if forAddr.Is4() {
		gen = g.v4
	}
	g.mu.Unlock()
	if gen == nil {
		return netip.Addr{}, fmt.Errorf("no virtual subnet is configured for %s", forAddr)
	}
	return gen.Next()
}

// Subnets returns the subnet of each generator that has been created.
func (g *Generators) Subnets() []netip.Prefix {
	g.mu.Lock()
	defer g.mu.Unlock()
	sns := make([]netip.Prefix, 0, 2)
	if g.v4 != nil {
		sns = append(sns, g.v4.Subnet())
	}
	if g.v6 != nil {
		sns = append(sns, g.v6.Subnet())
	}
	return sns
}

// NewGenerator creates a generator for virtual IPs with in the given subnet.
func NewGenerator(sn netip.Prefix) Generator {
	lo := sn.Masked().Addr()
	// Allocate from the lower half of the subnet, reserving the upper half for the
	// device's owned source addresses, which count down from the top of the same
	// range (see pkg/vif ownedAddress). This guarantees a virtual IP can never
	// collide with an owned address. A subnet too small to split is used whole.
	alloc := sn
	if sn.Bits() < sn.Addr().BitLen() {
		alloc = netip.PrefixFrom(lo, sn.Bits()+1)
	}
	if lo.Is4() {
		return &ip4Generator{
			subnet:        sn,
			alloc:         alloc,
			nextVirtualIP: intFromIPV4(lo),
		}
	}
	fixed, loInt := intsFromIPV6(lo)
	return &vip6Provider{
		subnet:  sn,
		alloc:   alloc,
		fixedHi: fixed,
		nextLo:  loInt,
	}
}

type ip4Generator struct {
	subnet        netip.Prefix
	alloc         netip.Prefix
	nextVirtualIP uint32
}

func (v *ip4Generator) Next() (netip.Addr, error) {
	nxt := ipV4FromInt(atomic.AddUint32(&v.nextVirtualIP, 1))
	if !v.alloc.Contains(nxt) {
		return netip.Addr{}, fmt.Errorf("virtual subnet CIDR %s is exhausted", v.Subnet())
	}
	return nxt, nil
}

func (v *ip4Generator) Subnet() netip.Prefix {
	return v.subnet
}

func ipV4FromInt(v uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{
		byte(v & 0xff000000 >> 24),
		byte(v & 0x00ff0000 >> 16),
		byte(v & 0x0000ff00 >> 8),
		byte(v & 0x000000ff),
	})
}

func intFromIPV4(a netip.Addr) uint32 {
	v := a.As4()
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

type vip6Provider struct {
	subnet  netip.Prefix
	alloc   netip.Prefix
	fixedHi uint64
	nextLo  uint64
}

func (v *vip6Provider) Next() (netip.Addr, error) {
	nxt := ipV6FromInts(v.fixedHi, atomic.AddUint64(&v.nextLo, 1))
	if !v.alloc.Contains(nxt) {
		return netip.Addr{}, fmt.Errorf("virtual subnet CIDR %s is exhausted", v.Subnet())
	}
	return nxt, nil
}

func (v *vip6Provider) Subnet() netip.Prefix {
	return v.subnet
}

func ipV6FromInts(hi, lo uint64) netip.Addr {
	return netip.AddrFrom16([16]byte{
		byte(hi & 0xff00000000000000 >> 56),
		byte(hi & 0x00ff000000000000 >> 48),
		byte(hi & 0x0000ff0000000000 >> 40),
		byte(hi & 0x000000ff00000000 >> 32),
		byte(hi & 0x00000000ff000000 >> 24),
		byte(hi & 0x0000000000ff0000 >> 16),
		byte(hi & 0x000000000000ff00 >> 8),
		byte(hi & 0x00000000000000ff),
		byte(lo & 0xff00000000000000 >> 56),
		byte(lo & 0x00ff000000000000 >> 48),
		byte(lo & 0x0000ff0000000000 >> 40),
		byte(lo & 0x000000ff00000000 >> 32),
		byte(lo & 0x00000000ff000000 >> 24),
		byte(lo & 0x0000000000ff0000 >> 16),
		byte(lo & 0x000000000000ff00 >> 8),
		byte(lo & 0x00000000000000ff),
	})
}

func intsFromIPV6(a netip.Addr) (uint64, uint64) {
	v := a.As16()
	return uint64(v[0])<<56 | uint64(v[1])<<48 | uint64(v[2])<<40 | uint64(v[3])<<32 | uint64(v[4])<<24 | uint64(v[5])<<16 | uint64(v[6])<<8 | uint64(v[7]),
		uint64(v[8])<<56 | uint64(v[9])<<48 | uint64(v[10])<<40 | uint64(v[11])<<32 | uint64(v[12])<<24 | uint64(v[13])<<16 | uint64(v[14])<<8 | uint64(v[15])
}

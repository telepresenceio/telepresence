package vip

import (
	"fmt"
	"net"
	"sync/atomic"

	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type Generator interface {
	Next() (net.IP, error)
	Subnet() *net.IPNet
}

// NewGenerator creates a generator for virtual IPs with in the given subnet.
func NewGenerator(sn *net.IPNet) Generator {
	lo := sn.IP.Mask(sn.Mask)
	hi := subnet.MaxIP(sn)
	if len(lo) == 4 {
		return &ip4Generator{
			subnet:        *sn,
			nextVirtualIP: intFromIPV4(lo),
			maxVirtualIP:  intFromIPV4(hi) + 1,
		}
	} else {
		fixed, lo := intsFromIPV6(lo)
		_, maxLo := intsFromIPV6(hi)
		return &vip6Provider{
			subnet:  *sn,
			fixedHi: fixed,
			nextLo:  lo,
			maxLo:   maxLo,
		}
	}
}

type ip4Generator struct {
	subnet        net.IPNet
	maxVirtualIP  uint32 // Immutable
	nextVirtualIP uint32
}

func (v *ip4Generator) Next() (net.IP, error) {
	nxt := atomic.AddUint32(&v.nextVirtualIP, 1)
	if nxt >= v.maxVirtualIP {
		return nil, fmt.Errorf("virtual subnet CIDR %s is exhausted", v.Subnet())
	}
	return ipV4FromInt(nxt), nil
}

func (v *ip4Generator) Subnet() *net.IPNet {
	return &v.subnet
}

func ipV4FromInt(v uint32) net.IP {
	return net.IP{
		byte(v & 0xff000000 >> 24),
		byte(v & 0x00ff0000 >> 16),
		byte(v & 0x0000ff00 >> 8),
		byte(v & 0x000000ff),
	}
}

func intFromIPV4(v net.IP) uint32 {
	return uint32(v[0])<<24 | uint32(v[1])<<16 | uint32(v[2])<<8 | uint32(v[3])
}

type vip6Provider struct {
	subnet  net.IPNet
	fixedHi uint64
	maxLo   uint64 // Immutable
	nextLo  uint64
}

func (v *vip6Provider) Next() (net.IP, error) {
	nxt := atomic.AddUint64(&v.nextLo, 1)
	if nxt >= v.maxLo {
		return nil, fmt.Errorf("virtual subnet CIDR %s is exhausted", v.Subnet())
	}
	return ipV6FromInts(v.fixedHi, nxt), nil
}

func (v *vip6Provider) Subnet() *net.IPNet {
	return &v.subnet
}

func ipV6FromInts(hi, lo uint64) net.IP {
	return net.IP{
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
	}
}

func intsFromIPV6(v net.IP) (uint64, uint64) {
	return uint64(v[0])<<56 | uint64(v[1])<<48 | uint64(v[2])<<40 | uint64(v[3])<<32 | uint64(v[4])<<24 | uint64(v[5])<<16 | uint64(v[6])<<8 | uint64(v[7]),
		uint64(v[8])<<56 | uint64(v[9])<<48 | uint64(v[10])<<40 | uint64(v[11])<<32 | uint64(v[12])<<24 | uint64(v[13])<<16 | uint64(v[14])<<8 | uint64(v[15])
}

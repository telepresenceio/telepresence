package rootd

import (
	"errors"
	"net"
	"sync/atomic"

	"github.com/telepresenceio/telepresence/v2/pkg/subnet"
)

type vipProvider struct {
	net.IPNet
	maxVirtualIP  uint32 // Immutable
	nextVirtualIP uint32
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

func newVipProvider(sn *net.IPNet) *vipProvider {
	lo := sn.IP.Mask(sn.Mask)
	hi := subnet.MaxIP(sn)
	return &vipProvider{
		IPNet:         *sn,
		nextVirtualIP: intFromIPV4(lo),
		maxVirtualIP:  intFromIPV4(hi) + 1,
	}
}

func (v *vipProvider) nextVIP() (net.IP, error) {
	nxt := atomic.AddUint32(&v.nextVirtualIP, 1)
	if nxt >= v.maxVirtualIP {
		return nil, errors.New("virtual subnet CIDR %s is exhausted")
	}
	return ipV4FromInt(nxt), nil
}

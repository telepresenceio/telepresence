// Package subnet contains functions for finding available subnets
package subnet

import (
	"bytes"
	"net"
	"sort"
)

// CoveringCIDRs returns the ip networks needed to cover the given IPs with as
// big mask as possible for each subnet. The analyze starts by finding all
// subnets using a 16-bit mask for IPv4 and a 64 bit mask for IPv6 addresses.
// Once the subnets are established, the mask for each one will be increased
// to the maximum value that still masks all IPs that it was created for.
//
// Note: A similar method exists in Telepresence 1, but this method was not
// compared to it when written.
func CoveringCIDRs(ips []net.IP) []*net.IPNet {
	// IPv4 subnet key. Identifies a class B subnet
	type ipv4SubnetKey [2]byte

	// IPv6 subnet key. This is the 48-bit route and 16-bit subnet identifier. Identifies a 64 bit subnet.
	type ipv6SubnetKey [8]byte

	ipv6Subnets := make(map[ipv6SubnetKey]*[7]Bitfield256)

	// Divide into subnets with ByteSets.

	// IPv4 has 2 byte subnets and one Bitfield256 representing the third byte.
	// (last byte is skipped because no split on subnet is made on that byte).
	ipv4Subnets := make(map[ipv4SubnetKey]*Bitfield256)

	// IPv6 has 8 byte subnets and seven ByteSets representing all but the last
	// byte of the subnet relative 64 bit address (last byte is skipped because
	// no split into subnets is made using that byte).
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			bk := ipv4SubnetKey{ip4[0], ip4[1]}
			var bytes *Bitfield256
			if bytes = ipv4Subnets[bk]; bytes == nil {
				bytes = &Bitfield256{}
				ipv4Subnets[bk] = bytes
			}
			bytes.SetBit(ip4[2])
		} else if ip16 := ip.To16(); ip16 != nil {
			r := ipv6SubnetKey{}
			copy(r[:], ip16)
			byteSets, ok := ipv6Subnets[r]
			if !ok {
				byteSets = &[7]Bitfield256{}
				ipv6Subnets[r] = byteSets
			}
			for i := range byteSets {
				byteSets[i].SetBit(ip16[i+8])
			}
		}
	}

	subnets := make([]*net.IPNet, len(ipv4Subnets)+len(ipv6Subnets))
	i := 0
	for network, bytes := range ipv4Subnets {
		ones, thirdByte := bytes.Mask()
		subnets[i] = &net.IPNet{
			IP:   net.IP{network[0], network[1], thirdByte, 0},
			Mask: net.CIDRMask(16+ones, 32),
		}
		i++
	}
	for subnet, byteSets := range ipv6Subnets {
		maskOnes := 64
		ip := make(net.IP, 16)
		copy(ip, subnet[:])
		for bi, bytes := range byteSets {
			ones, nByte := bytes.Mask()
			maskOnes += ones
			ip[8+bi] = nByte
			if ones != 8 {
				break
			}
		}
		subnets[i] = &net.IPNet{
			IP:   ip,
			Mask: net.CIDRMask(maskOnes, 128),
		}
		i++
	}
	sort.Slice(subnets, func(i, j int) bool { return compareIPs(subnets[i].IP, subnets[j].IP) < 0 })
	return subnets
}

// compareIPs is like bytes.Compare but will always consider IPv4 less than IPv6.
func compareIPs(a, b net.IP) int {
	dl := len(a) - len(b)
	switch {
	case dl == 0:
		dl = bytes.Compare(a, b)
	case dl < 0:
		dl = -1
	default:
		dl = 1
	}
	return dl
}

// Covers answers the question if network range a contains all of network range b
func Covers(a, b *net.IPNet) bool {
	if !a.Contains(b.IP) {
		return false
	}

	// create max IP in range b using its mask
	ones, _ := b.Mask.Size()
	l := len(b.IP)
	m := make(net.IP, l)
	n := uint(ones)
	for i := 0; i < l; i++ {
		switch {
		case n >= 8:
			m[i] = b.IP[i]
			n -= 8
		case n > 0:
			m[i] = b.IP[i] | byte(0xff>>n)
			n = 0
		default:
			m[i] = 0xff
		}
	}
	return a.Contains(m)
}

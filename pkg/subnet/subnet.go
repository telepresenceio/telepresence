// Package subnet contains functions for finding available subnets
package subnet

import (
	"bytes"
	"fmt"
	"net"
	"sort"
)

// CoveringCIDRs returns the ip networks needed to cover the given IPs with as
// big mask as possible for each subnet. The analysis starts by finding all
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
	// byte of the subnet relative 64-bit address (last byte is skipped because
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

// Unique will drop any subnet that is covered by another subnet from the
// given slice and return the resulting slice. This function will alter
// the given slice.
func Unique(subnets []*net.IPNet) []*net.IPNet {
	ln := len(subnets)
	for i, isn := range subnets {
		if i >= ln {
			break
		}
		for r, rsn := range subnets {
			if i == r {
				continue
			}
			if Covers(rsn, isn) {
				ln--
				subnets[i] = subnets[ln]
				break
			}
		}
	}
	return subnets[:ln]
}

// Partition returns two slices, the first containing the subnets for which the filter evaluates
// to true, the second containing the rest.
func Partition(subnets []*net.IPNet, filter func(int, *net.IPNet) bool) (matched, notMatched []*net.IPNet) {
	for i, sn := range subnets {
		if filter(i, sn) {
			matched = append(matched, sn)
		} else {
			notMatched = append(notMatched, sn)
		}
	}
	return
}

// Equal returns true if a and b have equal IP and masks.
func Equal(a, b *net.IPNet) bool {
	if a.IP.Equal(b.IP) {
		ao, ab := a.Mask.Size()
		bo, bb := b.Mask.Size()
		return ao == bo && ab == bb
	}
	return false
}

// Covers answers the question if network range a contains the full network range b.
func Covers(a, b *net.IPNet) bool {
	return a.Contains(b.IP) && a.Contains(MaxIP(b))
}

// Overlaps answers the question if there is an overlap between network range a and b.
func Overlaps(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || a.Contains(MaxIP(b)) || b.Contains(a.IP) || b.Contains(MaxIP(a))
}

func MaxIP(cidr *net.IPNet) net.IP {
	// create max IP in range b using its mask
	ones, _ := cidr.Mask.Size()
	l := len(cidr.IP)
	m := make(net.IP, l)
	n := uint(ones)
	for i := 0; i < l; i++ {
		switch {
		case n >= 8:
			m[i] = cidr.IP[i]
			n -= 8
		case n > 0:
			m[i] = cidr.IP[i] | byte(0xff>>n)
			n = 0
		default:
			m[i] = 0xff
		}
	}
	return m
}

// incIP attempts to increase the given ip. The increase starts at the penultimate byte. The increased IP is
// returned unless it is equal or larger than the given end, in which case nil is returned.
func incIP(ip, end net.IP) net.IP {
	ipc := make(net.IP, len(ip))
	for bi := len(ip) - 2; bi >= 0; bi-- {
		if bv := ip[bi]; bv < 255 {
			copy(ipc, ip)
			ipc[bi] = bv + 1
			// set bytes to the right of the increased byt to zero.
			for xi := bi + 1; xi < len(ipc)-1; xi++ {
				ipc[xi] = 0
			}
			if compareIPs(ipc, end) < 0 {
				return ipc
			}
			break
		}
	}
	return nil
}

// RandomIPv4Subnet finds a random free subnet using the given mask. A subnet is considered
// free if it doesn't overlap with any of the subnets returned by the net.InterfaceAddrs
// function or with any of the subnets provided in the avoid parameter.
// The returned subnet will be a private IPv4 subnet in either class C, B, or A range, and the search
// for a free subnet uses that order.
// See https://en.wikipedia.org/wiki/Private_network for more info about private subnets.
func RandomIPv4Subnet(mask net.IPMask, avoid []*net.IPNet) (*net.IPNet, error) {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	cidrs := make([]*net.IPNet, 0, len(as)+len(avoid))
	for _, a := range as {
		if _, cidr, err := net.ParseCIDR(a.String()); err == nil {
			cidrs = append(cidrs, cidr)
		}
	}
	cidrs = append(cidrs, avoid...)

	// IP address range pairs, from - to (to is non-inclusive)
	ranges := []net.IP{
		{192, 168, 0, 0}, {192, 169, 0, 0}, // Class C private range
		{172, 16, 0, 0}, {172, 32, 0, 0}, // Class B private range
		{10, 0, 0, 0}, {11, 0, 0, 0}, // Class A private range
	}

	for i := 0; i < len(ranges); i += 2 {
		ip := ranges[i]

		end := ranges[i+1]
		inUse := false
		for {
			ip1 := make(net.IP, len(ip))
			copy(ip1, ip)
			ip1[len(ip)-1] = 1
			sn := net.IPNet{
				IP:   ip1,
				Mask: mask,
			}
			for _, cidr := range cidrs {
				if Overlaps(cidr, &sn) {
					inUse = true
					break
				}
			}
			if !inUse {
				return &sn, nil
			}
			if ip = incIP(ip, end); ip == nil {
				break
			}
		}
	}
	return nil, fmt.Errorf("unable to find a free subnet")
}

func IsZeroMask(n *net.IPNet) bool {
	for _, b := range n.Mask {
		if b != 0 {
			return false
		}
	}
	return true
}

// IsHalfOfDefault route returns true if the given subnet covers half the address space with a /1 mask.
func IsHalfOfDefault(n *net.IPNet) bool {
	ones, _ := n.Mask.Size()
	return ones == 1
}

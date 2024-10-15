// Package subnet contains functions for finding available subnets
package subnet

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
)

// CoveringPrefixes returns the ip networks needed to cover the given IPs with as
// big mask as possible for each subnet. The analysis starts by finding all
// subnets using a 16-bit mask for IPv4 and a 64 bit mask for IPv6 addresses.
// Once the subnets are established, the mask for each one will be increased
// to the maximum value that still masks all IPs that it was created for.
func CoveringPrefixes(addrs []netip.Addr) []netip.Prefix {
	// Divide into subnets with ByteSets.
	// IPv4 subnet key. Identifies a class B subnet
	type ipv4SubnetKey [2]byte

	// IPv6 subnet key. This is the 48-bit route and 16-bit subnet identifier. Identifies a 64 bit subnet.
	type ipv6SubnetKey [8]byte

	ipv6Subnets := make(map[ipv6SubnetKey]*[7]Bitfield256)

	// IPv4 has 2 byte subnets and one Bitfield256 representing the third byte.
	// (last byte is skipped because no split on subnet is made on that byte).
	ipv4Subnets := make(map[ipv4SubnetKey]*Bitfield256)

	// IPv6 has 8 byte subnets and seven ByteSets representing all but the last
	// byte of the subnet relative 64-bit address (last byte is skipped because
	// no split into subnets is made using that byte).
	for _, ip := range addrs {
		if ip.Is4() {
			ip4 := ip.As4()
			var bytes *Bitfield256
			r := ipv4SubnetKey{ip4[0], ip4[1]}
			if bytes = ipv4Subnets[r]; bytes == nil {
				bytes = &Bitfield256{}
				ipv4Subnets[r] = bytes
			}
			bytes.SetBit(ip4[2])
		} else if ip.Is6() {
			ip16 := ip.As16()
			r := ipv6SubnetKey{}
			copy(r[:], ip16[:8])
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

	subnets := make([]netip.Prefix, len(ipv4Subnets)+len(ipv6Subnets))
	i := 0
	for network, bytes := range ipv4Subnets {
		ones, thirdByte := bytes.Mask()
		subnets[i] = netip.PrefixFrom(netip.AddrFrom4([4]byte{network[0], network[1], thirdByte, 0}), 16+ones)
		i++
	}
	for subnet, byteSets := range ipv6Subnets {
		maskOnes := 64
		ip := [16]byte{}
		copy(ip[:], subnet[:])
		for bi, bytes := range byteSets {
			ones, nByte := bytes.Mask()
			maskOnes += ones
			ip[8+bi] = nByte
			if ones != 8 {
				break
			}
		}
		subnets[i] = netip.PrefixFrom(netip.AddrFrom16(ip), maskOnes)
		i++
	}
	sort.Slice(subnets, func(i, j int) bool { return subnets[i].Addr().Less(subnets[j].Addr()) })
	return subnets
}

// Unique will drop any subnet that is covered by another subnet from the
// given slice and return the resulting slice. This function will alter
// the given slice.
func Unique(subnets []netip.Prefix) []netip.Prefix {
	ln := len(subnets)
	for i := 0; i < ln; i++ {
		for r := 0; r < ln; r++ {
			if r != i && Covers(subnets[r], subnets[i]) {
				ln--
				if i < ln {
					subnets[i] = subnets[ln]
					i--
				}
				break
			}
		}
	}
	return subnets[:ln]
}

// Partition returns two slices, the first containing the subnets for which the filter evaluates
// to true, the second containing the rest.
func Partition[T any](subnets []T, filter func(int, T) bool) (matched, notMatched []T) {
	for i, sn := range subnets {
		if filter(i, sn) {
			matched = append(matched, sn)
		} else {
			notMatched = append(notMatched, sn)
		}
	}
	return
}

// Covers answers the question if network range a contains the full network range b.
func Covers(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) && a.Contains(PrefixMaxIP(b))
}

func PrefixMaxIP(cidr netip.Prefix) netip.Addr {
	// create max IP in range b using its mask
	a := cidr.Addr()
	l := a.BitLen()
	m := a.AsSlice()
	for i := cidr.Bits(); i < l; i++ {
		byn := i / 8 // byte number
		bit := i % 8 // the bit in that byte
		if bit == 7 {
			m[byn] = 0xff // set all eight bits
			i += 7        // and jump to the next byte
		} else {
			m[byn] |= 1 << bit
		}
	}
	mx, _ := netip.AddrFromSlice(m)
	return mx
}

// incIP4 attempts to increase the given ip. The increase starts at the penultimate byte. The increased IP is
// returned unless it is equal or larger than the given end, in which case nil is returned.
func incIP4(ip, end netip.Addr) netip.Addr {
	ipc := ip.As4()
	for bi := 2; bi >= 0; bi-- {
		if bv := ipc[bi]; bv < 255 {
			ipc[bi] = bv + 1
			// set bytes to the right of the increased byte to zero.
			for xi := bi + 1; xi < len(ipc)-1; xi++ {
				ipc[xi] = 0
			}
			ip = netip.AddrFrom4(ipc)
			if ip.Less(end) {
				return ip
			}
			break
		}
	}
	return netip.Addr{}
}

// RandomIPv4Prefix finds a random free subnet using the given mask. A subnet is considered
// free if it doesn't overlap with any of the subnets returned by the net.InterfaceAddrs
// function or with any of the subnets provided in the avoid parameter.
// The returned subnet will be a private IPv4 subnet in either class C, B, or A range, and the search
// for a free subnet uses that order.
// See https://en.wikipedia.org/wiki/Private_network for more info about private subnets.
func RandomIPv4Prefix(bits int, avoid []netip.Prefix) (netip.Prefix, error) {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return netip.Prefix{}, err
	}
	cidrs := make([]netip.Prefix, 0, len(as)+len(avoid))
	for _, a := range as {
		if cidr, err := netip.ParsePrefix(a.String()); err == nil {
			cidrs = append(cidrs, cidr)
		}
	}
	cidrs = append(cidrs, avoid...)

	// IP address range pairs, from - to (to is non-inclusive)
	ranges := []netip.Addr{
		netip.AddrFrom4([4]byte{192, 168, 0, 0}), netip.AddrFrom4([4]byte{192, 169, 0, 0}), // Class C private range
		netip.AddrFrom4([4]byte{172, 16, 0, 0}), netip.AddrFrom4([4]byte{172, 32, 0, 0}), // Class B private range
		netip.AddrFrom4([4]byte{10, 0, 0, 0}), netip.AddrFrom4([4]byte{11, 0, 0, 0}), // Class A private range
	}

	for i := 0; i < len(ranges); i += 2 {
		ip := ranges[i]

		end := ranges[i+1]
		for {
			ip1 := ip.As4()
			ip1[3] = 1
			sn := netip.PrefixFrom(netip.AddrFrom4(ip1), bits)
			inUse := false
			for _, cidr := range cidrs {
				if cidr.Overlaps(sn) {
					inUse = true
					break
				}
			}
			if !inUse {
				return sn, nil
			}
			if ip = incIP4(ip, end); ip.IsValid() {
				break
			}
		}
	}
	return netip.Prefix{}, fmt.Errorf("unable to find a free subnet")
}

// IsHalfOfDefault route returns true if the given subnet covers half the address space with a /1 mask.
func IsHalfOfDefault(n netip.Prefix) bool {
	return n.Bits() == 1
}

func PrefixToIPNet(p netip.Prefix) *net.IPNet {
	if !p.IsValid() {
		return nil
	}
	a := p.Addr()
	return &net.IPNet{
		IP:   a.AsSlice(),
		Mask: net.CIDRMask(p.Bits(), a.BitLen()),
	}
}

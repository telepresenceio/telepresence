// Package subnet contains functions for finding available subnets
package subnet

import "C"
import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"sort"
)

type ipAndNetwork struct {
	ip      net.IP
	network *net.IPNet
}

// stubbable version
var interfaceAddrs = net.InterfaceAddrs

// IPv4 subnet key. Identifies a class B subnet
type ipv4SubnetKey [2]byte

// IPv6 subnet key. This is the 48-bit route and 16-bit subnet identifier. Identifies a 64 bit subnet.
type ipv6SubnetKey [8]byte

// AnalyzeIPs returns the ip networks needed to cover the given IPs with as
// big mask as possible for each subnet. The analyze starts by finding all
// subnets using a 16-bit mask for IPv4 and a 64 bit mask for IPv6 addresses.
// Once the subnets are established, the mask for each one will be increased
// to the maximum value that still masks all IPs that it was created for.
func AnalyzeIPs(ips []net.IP) []*net.IPNet {
	ipv6Subnets := make(map[ipv6SubnetKey]*[7]ByteSet)

	// Divide into subnets with ByteSets.

	// IPv4 has 2 byte subnets and one ByteSet representing the third byte.
	// (last byte is skipped because no split on subnet is made on that byte).
	ipv4Subnets := make(map[ipv4SubnetKey]*ByteSet)

	// IPv6 has 8 byte subnets and seven ByteSets representing all but the last
	// byte of the subnet relative 64 bit address (last byte is skipped because
	// no split into subnets is made using that byte).
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			bk := ipv4SubnetKey{ip4[0], ip4[1]}
			var bytes *ByteSet
			if bytes = ipv4Subnets[bk]; bytes == nil {
				bytes = &ByteSet{}
				ipv4Subnets[bk] = bytes
			}
			bytes.Add(ip4[2])
		} else {
			r := ipv6SubnetKey{}
			copy(r[:], ip)
			byteSets, ok := ipv6Subnets[r]
			if !ok {
				byteSets = &[7]ByteSet{}
				ipv6Subnets[r] = byteSets
			}
			for i := range byteSets {
				byteSets[i].Add(ip[i+8])
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

// FindAvailableClassC returns the first class C subnet CIDR in the address ranges reserved
// for private (non-routed) use that isn't in use by an existing network interface.
func FindAvailableClassC() (*net.IPNet, error) {
	addrs, err := interfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("failed to obtain interface addresses: %v", err)
	}

	cidrs := make([]*ipAndNetwork, 0, len(addrs))
	for _, a := range addrs {
		if ip, network, err := net.ParseCIDR(a.String()); err == nil {
			cidrs = append(cidrs, &ipAndNetwork{ip: ip, network: network})
		}
	}
	for i := 0; i < 256; i++ {
		if found := findChunk(cidrs, 10, i); found >= 0 {
			return cidr24(10, i, found), nil
		}
	}
	for i := 16; i < 32; i++ {
		if found := findChunk(cidrs, 17, i); found >= 0 {
			return cidr24(17, i, found), nil
		}
	}
	if found := findChunk(cidrs, 192, 168); found >= 0 {
		return cidr24(192, 168, found), nil
	}
	return nil, errors.New("no available CIDR")
}

func Available(subnet *net.IPNet) (bool, error) {
	addrs, err := interfaceAddrs()
	if err != nil {
		return false, fmt.Errorf("failed to obtain interface addresses: %v", err)
	}

	for _, a := range addrs {
		_, network, err := net.ParseCIDR(a.String())
		if err != nil {
			return false, fmt.Errorf("failed to parse interface address: %v", err)
		}
		if covers(network, subnet) {
			return false, nil
		}
	}
	return true, nil
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

func findChunk(cidrs []*ipAndNetwork, ar1, ar2 int) int {
	_, wantedRange, err := net.ParseCIDR(fmt.Sprintf("%d.%d.0.0/16", ar1, ar2))
	if err != nil {
		panic(err)
	}
	return findAvailableChunk(wantedRange, cidrs)
}

func cidr24(ar1, ar2, ar3 int) *net.IPNet {
	ip := make(net.IP, 4)
	ip[0] = byte(ar1)
	ip[1] = byte(ar2)
	ip[2] = byte(ar3)
	return &net.IPNet{
		IP:   ip,
		Mask: net.CIDRMask(24, 32),
	}
}

// covers answers the question if network range a contains all of network range b
func covers(a, b *net.IPNet) bool {
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

func findAvailableChunk(wantedRange *net.IPNet, cidrs []*ipAndNetwork) int {
	inUse := [256]bool{}
	for _, cid := range cidrs {
		if covers(cid.network, wantedRange) {
			return -1
		}
		if !wantedRange.Contains(cid.ip) {
			continue
		}
		ones, bits := cid.network.Mask.Size()
		if bits != 32 {
			return -1
		}
		if ones >= 24 {
			inUse[cid.network.IP[2]] = true
		} else {
			ones -= 16
			mask := 0xff >> ones
			for i := 0; i <= mask; i++ {
				inUse[i] = true
			}
		}
	}

	for i := 0; i < 256; i++ {
		if !inUse[i] {
			return i
		}
	}
	return -1
}

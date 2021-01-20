// Package subnet contains functions for finding available subnets
package subnet

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

type ipAndNetwork struct {
	ip      net.IP
	network *net.IPNet
}

// stubbable version
var interfaceAddrs = net.InterfaceAddrs

// FindAvailableClassC returns the first class C subnet CIDR in the address ranges reserved
// for private (non-routed) use that isn't in use by an existing network interface.
func FindAvailableClassC() (string, error) {
	addrs, err := interfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("failed to obtain interface addresses: %v", err)
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
	return "", errors.New("no available CIDR")
}

// FindAvailableLoopBackClassC returns the first class C subnet CIDR in the address ranges reserved
// for private (non-routed) use that isn't in use by an existing network interface.
func FindAvailableLoopBackClassC() (string, error) {
	addrs, err := interfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("failed to obtain interface addresses: %v", err)
	}

	cidrs := make([]*ipAndNetwork, 0)
	for _, a := range addrs {
		s := a.String()
		if !strings.HasPrefix(s, "127.") {
			// Only consider ipv4 loopback addresses
			continue
		}
		var ip net.IP
		if ip, _, err = net.ParseCIDR(s); err != nil {
			continue
		}

		m := net.CIDRMask(onesFromEight(ip), 32)
		cidrs = append(cidrs, &ipAndNetwork{ip: ip, network: &net.IPNet{
			IP:   ip.Mask(m),
			Mask: m,
		}})
	}

	for i := 0; i < 256; i++ {
		if found := findChunk(cidrs, 127, i); found >= 0 {
			return cidr24(127, i, found), nil
		}
	}
	return "", errors.New("no available CIDR")
}

// onesFromEight computes the number of ones needed to mask everything from
// the first bit found in the second and third byte of the IP address. I.e. the
// number of ones will vary between 8 and 24.
func onesFromEight(ip net.IP) int {
	ones := 8
	for bt := 1; bt < 3; bt++ {
		ip1 := int(ip[1])
		for bit := 7; bit >= 0; bit-- {
			if ip1>>bit != 0 {
				return ones
			}
			ones++
		}
	}
	return ones
}

func findChunk(cidrs []*ipAndNetwork, ar1, ar2 int) int {
	_, wantedRange, err := net.ParseCIDR(fmt.Sprintf("%d.%d.0.0/16", ar1, ar2))
	if err != nil {
		panic(err)
	}
	return findAvailableChunk(wantedRange, cidrs)
}

func cidr24(ar1, ar2, ar3 int) string {
	return fmt.Sprintf("%d.%d.%d.0/24", ar1, ar2, ar3)
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

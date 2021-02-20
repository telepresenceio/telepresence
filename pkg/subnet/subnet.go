// Package subnet contains functions for finding available subnets
package subnet

import (
	"errors"
	"fmt"
	"net"
)

type ipAndNetwork struct {
	ip      net.IP
	network *net.IPNet
}

// stubbable version
var interfaceAddrs = net.InterfaceAddrs

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

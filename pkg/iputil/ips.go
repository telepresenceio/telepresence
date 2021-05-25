package iputil

import (
	"bytes"
	"net"
	"sort"
	"strings"
)

type IPs []net.IP

func IPsFromBytesSlice(bss [][]byte) IPs {
	ips := make(IPs, len(bss))
	for i, bs := range bss {
		ips[i] = bs
	}
	return ips
}

func (ips IPs) String() string {
	nips := len(ips)
	switch nips {
	case 0:
		return ""
	case 1:
		return ips[0].String()
	default:
		sb := strings.Builder{}
		sb.WriteString(ips[0].String())
		for i := 1; i < nips; i++ {
			sb.WriteByte(',')
			sb.WriteString(ips[i].String())
		}
		return sb.String()
	}
}

func (ips IPs) UniqueSorted() IPs {
	sort.Slice(ips, func(i, j int) bool {
		return bytes.Compare(ips[i], ips[j]) < 0
	})
	var prev net.IP
	last := len(ips) - 1
	for i := 0; i <= last; i++ {
		s := ips[i]
		if s.Equal(prev) {
			copy(ips[i:], ips[i+1:])
			last--
			i--
		} else {
			prev = s
		}
	}
	return ips[:last+1]
}

// BytesSlice is returns a [][]byte copy of the IPs
func (ips IPs) BytesSlice() [][]byte {
	bss := make([][]byte, len(ips))
	for i, bs := range ips {
		bss[i] = bs
	}
	return bss
}

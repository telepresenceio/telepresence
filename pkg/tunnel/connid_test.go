package tunnel

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/v2/pkg/ipproto"
)

var (
	ipv4a     = netip.MustParseAddr("192.168.0.1")
	ipv4b     = netip.MustParseAddr("192.168.3.8")
	ipv6a     = netip.MustParseAddr("2a05:d014:153d:d732:a2d3::15")
	ipv6b     = netip.MustParseAddr("2a05:d014:153d:d732:a2f2::08")
	ipv4Asv6a = netip.AddrFrom16(ipv4a.As16())
	ipv4Asv6b = netip.AddrFrom16(ipv4b.As16())
)

func TestConnIDFromUDP(t *testing.T) {
	tests := []struct {
		name string
		src  netip.AddrPort
		dst  netip.AddrPort
		want string
	}{
		{
			name: "ipv4-ipv4",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: "udp 192.168.0.1:4 -> 192.168.3.8:8",
		},
		{
			name: "ipv4-ipv6",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: "udp 192.168.0.1:4 -> [2a05:d014:153d:d732:a2f2::8]:8",
		},
		{
			name: "ipv4-ipv4as6",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: "udp 192.168.0.1:4 -> 192.168.3.8:8",
		},
		{
			name: "ipv6-ipv4",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: "udp [2a05:d014:153d:d732:a2d3::15]:4 -> 192.168.3.8:8",
		},
		{
			name: "ipv6-ipv6",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: "udp [2a05:d014:153d:d732:a2d3::15]:4 -> [2a05:d014:153d:d732:a2f2::8]:8",
		},
		{
			name: "ipv6-ipv4as6",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: "udp [2a05:d014:153d:d732:a2d3::15]:4 -> 192.168.3.8:8",
		},
		{
			name: "ipv4as6-ipv4",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: "udp 192.168.0.1:4 -> 192.168.3.8:8",
		},
		{
			name: "ipv4as6-ipv6",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: "udp 192.168.0.1:4 -> [2a05:d014:153d:d732:a2f2::8]:8",
		},
		{
			name: "ipv4as6-ipv4as6",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: "udp 192.168.0.1:4 -> 192.168.3.8:8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, ConnIDFromUDP(tt.src, tt.dst).String(), "ConnIDFromUDP(%v, %v)", tt.src, tt.dst)
		})
	}
}

func TestConnID_Source(t *testing.T) {
	tests := []struct {
		name string
		src  netip.AddrPort
		dst  netip.AddrPort
		want netip.AddrPort
	}{
		{
			name: "ipv4-ipv4",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: netip.AddrPortFrom(ipv4a, 4),
		},
		{
			name: "ipv4-ipv6",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: netip.AddrPortFrom(ipv4a, 4),
		},
		{
			name: "ipv4-ipv4as6",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: netip.AddrPortFrom(ipv4a, 4),
		},
		{
			name: "ipv6-ipv4",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: netip.AddrPortFrom(ipv6a, 4),
		},
		{
			name: "ipv6-ipv6",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: netip.AddrPortFrom(ipv6a, 4),
		},
		{
			name: "ipv6-ipv4as6",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: netip.AddrPortFrom(ipv6a, 4),
		},
		{
			name: "ipv4as6-ipv4",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: netip.AddrPortFrom(ipv4a, 4),
		},
		{
			name: "ipv4as6-ipv6",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: netip.AddrPortFrom(ipv4a, 4),
		},
		{
			name: "ipv4as6-ipv4as6",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: netip.AddrPortFrom(ipv4a, 4),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, NewConnID(ipproto.TCP, tt.src, tt.dst).Source(), "Source()")
		})
	}
}

func TestConnID_Destination(t *testing.T) {
	tests := []struct {
		name string
		src  netip.AddrPort
		dst  netip.AddrPort
		want netip.AddrPort
	}{
		{
			name: "ipv4-ipv4",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: netip.AddrPortFrom(ipv4b, 8),
		},
		{
			name: "ipv4-ipv6",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: netip.AddrPortFrom(ipv6b, 8),
		},
		{
			name: "ipv4-ipv4as6",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: netip.AddrPortFrom(ipv4b, 8),
		},
		{
			name: "ipv6-ipv4",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: netip.AddrPortFrom(ipv4b, 8),
		},
		{
			name: "ipv6-ipv6",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: netip.AddrPortFrom(ipv6b, 8),
		},
		{
			name: "ipv6-ipv4as6",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: netip.AddrPortFrom(ipv4b, 8),
		},
		{
			name: "ipv4as6-ipv4",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: netip.AddrPortFrom(ipv4b, 8),
		},
		{
			name: "ipv4as6-ipv6",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: netip.AddrPortFrom(ipv6b, 8),
		},
		{
			name: "ipv4as6-ipv4as6",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: netip.AddrPortFrom(ipv4b, 8),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, NewConnID(ipproto.TCP, tt.src, tt.dst).Destination(), "Source()")
		})
	}
}

func TestConnID_areBothIPv4(t *testing.T) {
	tests := []struct {
		name string
		src  netip.AddrPort
		dst  netip.AddrPort
		want bool
	}{
		{
			name: "ipv4-ipv4",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: true,
		},
		{
			name: "ipv4-ipv6",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: false,
		},
		{
			name: "ipv4-ipv4as6",
			src:  netip.AddrPortFrom(ipv4a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: true,
		},
		{
			name: "ipv6-ipv4",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: false,
		},
		{
			name: "ipv6-ipv6",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: false,
		},
		{
			name: "ipv6-ipv4as6",
			src:  netip.AddrPortFrom(ipv6a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: false,
		},
		{
			name: "ipv4as6-ipv4",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv4b, 8),
			want: true,
		},
		{
			name: "ipv4as6-ipv6",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv6b, 8),
			want: false,
		},
		{
			name: "ipv4as6-ipv4as6",
			src:  netip.AddrPortFrom(ipv4Asv6a, 4),
			dst:  netip.AddrPortFrom(ipv4Asv6b, 8),
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, ConnIDFromUDP(tt.src, tt.dst).areBothIPv4(), "ConnIDFromUDP(%v, %v)", tt.src, tt.dst)
		})
	}
}

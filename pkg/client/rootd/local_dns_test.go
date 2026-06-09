package rootd

import (
	"context"
	"net/netip"
	"testing"
)

func mustAddrs(t *testing.T, ss ...string) []netip.Addr {
	t.Helper()
	addrs := make([]netip.Addr, len(ss))
	for i, s := range ss {
		addrs[i] = netip.MustParseAddr(s)
	}
	return addrs
}

func TestAppendLocalDNSNeverProxy(t *testing.T) {
	tests := []struct {
		name       string
		neverProxy []netip.Prefix
		subnets    []netip.Prefix
		dnsServers []netip.Addr
		want       []netip.Prefix
	}{
		{
			name:       "adds host route for DNS server inside a routed subnet",
			subnets:    []netip.Prefix{netip.MustParsePrefix("172.31.0.0/18")},
			dnsServers: mustAddrs(t, "172.31.0.2"),
			want:       []netip.Prefix{netip.MustParsePrefix("172.31.0.2/32")},
		},
		{
			name:       "ignores DNS server outside every routed subnet",
			subnets:    []netip.Prefix{netip.MustParsePrefix("172.31.0.0/18")},
			dnsServers: mustAddrs(t, "10.0.0.2"),
			want:       nil,
		},
		{
			name:       "skips loopback and unspecified servers",
			subnets:    []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("0.0.0.0/1")},
			dnsServers: mustAddrs(t, "127.0.0.53", "0.0.0.0"),
			want:       nil,
		},
		{
			name:       "does not duplicate an already configured never-proxy entry",
			neverProxy: []netip.Prefix{netip.MustParsePrefix("172.31.0.2/32")},
			subnets:    []netip.Prefix{netip.MustParsePrefix("172.31.0.0/18")},
			dnsServers: mustAddrs(t, "172.31.0.2"),
			want:       []netip.Prefix{netip.MustParsePrefix("172.31.0.2/32")},
		},
		{
			name:       "deduplicates repeated DNS server addresses",
			subnets:    []netip.Prefix{netip.MustParsePrefix("172.31.0.0/18")},
			dnsServers: mustAddrs(t, "172.31.0.2", "172.31.0.2"),
			want:       []netip.Prefix{netip.MustParsePrefix("172.31.0.2/32")},
		},
		{
			name:       "handles an IPv6 DNS server",
			subnets:    []netip.Prefix{netip.MustParsePrefix("fd00::/48")},
			dnsServers: mustAddrs(t, "fd00::53"),
			want:       []netip.Prefix{netip.MustParsePrefix("fd00::53/128")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := appendLocalDNSNeverProxy(context.Background(), tt.neverProxy, tt.subnets, tt.dnsServers)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

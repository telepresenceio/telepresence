package vif

import (
	"net/netip"
	"testing"

	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

func TestRouteViaDefaultPreservesGateway(t *testing.T) {
	sn := netip.MustParsePrefix("10.224.1.135/32")
	dr := routing.NewRoute(netip.MustParsePrefix("0.0.0.0/0"), 2, "enp1s0")
	dr.Gateway = netip.MustParseAddr("10.0.2.1")
	dr.LocalIP = netip.MustParseAddr("10.0.2.15")
	dr.Default = true

	r := routeViaDefault(sn, &dr)

	if r.RoutedNet != sn {
		t.Fatalf("RoutedNet = %s, want %s", r.RoutedNet, sn)
	}
	if r.InterfaceIndex != dr.InterfaceIndex {
		t.Fatalf("InterfaceIndex = %d, want %d", r.InterfaceIndex, dr.InterfaceIndex)
	}
	if r.InterfaceName != dr.InterfaceName {
		t.Fatalf("InterfaceName = %q, want %q", r.InterfaceName, dr.InterfaceName)
	}
	if r.Gateway != dr.Gateway {
		t.Fatalf("Gateway = %s, want %s", r.Gateway, dr.Gateway)
	}
	if r.LocalIP != dr.LocalIP {
		t.Fatalf("LocalIP = %s, want %s", r.LocalIP, dr.LocalIP)
	}
	if r.Default {
		t.Fatal("routeViaDefault returned a default route")
	}
}

func TestRouteViaDefaultDoesNotMixAddressFamilies(t *testing.T) {
	sn := netip.MustParsePrefix("fdea:9f85:dc1e::1/128")
	dr := routing.NewRoute(netip.MustParsePrefix("0.0.0.0/0"), 2, "enp1s0")
	dr.Gateway = netip.MustParseAddr("10.0.2.1")
	dr.LocalIP = netip.MustParseAddr("10.0.2.15")

	r := routeViaDefault(sn, &dr)

	if r.Gateway != netip.IPv6Unspecified() {
		t.Fatalf("Gateway = %s, want %s", r.Gateway, netip.IPv6Unspecified())
	}
	if r.LocalIP != netip.IPv6Unspecified() {
		t.Fatalf("LocalIP = %s, want %s", r.LocalIP, netip.IPv6Unspecified())
	}
}

func TestRouteViaPreservesNonDefaultRoute(t *testing.T) {
	sn := netip.MustParsePrefix("10.96.0.37/32")
	br := routing.NewRoute(netip.MustParsePrefix("10.96.0.0/16"), 11, "brm")
	br.LocalIP = netip.MustParseAddr("10.96.0.1")

	r := routeVia(sn, &br)

	if r.RoutedNet != sn {
		t.Fatalf("RoutedNet = %s, want %s", r.RoutedNet, sn)
	}
	if r.InterfaceIndex != br.InterfaceIndex {
		t.Fatalf("InterfaceIndex = %d, want %d", r.InterfaceIndex, br.InterfaceIndex)
	}
	if r.InterfaceName != br.InterfaceName {
		t.Fatalf("InterfaceName = %q, want %q", r.InterfaceName, br.InterfaceName)
	}
	if r.LocalIP != br.LocalIP {
		t.Fatalf("LocalIP = %s, want %s", r.LocalIP, br.LocalIP)
	}
}

func TestMostSpecificRoute(t *testing.T) {
	defaultRoute := routing.NewRoute(netip.MustParsePrefix("0.0.0.0/0"), 2, "enp1s0")
	defaultRoute.Default = true
	tunRoute := routing.NewRoute(netip.MustParsePrefix("172.31.0.0/18"), 5, "tel0")
	vethRoute := routing.NewRoute(netip.MustParsePrefix("172.31.0.0/18"), 7, "brm")
	narrowRoute := routing.NewRoute(netip.MustParsePrefix("172.31.0.0/24"), 8, "brm")

	addr := netip.MustParseAddr("172.31.0.2")

	tests := []struct {
		name  string
		table []*routing.Route
		want  *routing.Route
	}{
		{
			name:  "prefers a non-tunnel route over the default route",
			table: []*routing.Route{&defaultRoute, &tunRoute, &vethRoute},
			want:  &vethRoute,
		},
		{
			name:  "ignores routes on our own device",
			table: []*routing.Route{&defaultRoute, &tunRoute},
			want:  nil,
		},
		{
			name:  "picks the most specific matching route",
			table: []*routing.Route{&defaultRoute, &vethRoute, &narrowRoute},
			want:  &narrowRoute,
		},
		{
			name:  "returns nil when only the default route matches",
			table: []*routing.Route{&defaultRoute},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mostSpecificRoute(tt.table, addr, "tel0")
			if got != tt.want {
				t.Fatalf("mostSpecificRoute() = %v, want %v", got, tt.want)
			}
		})
	}
}

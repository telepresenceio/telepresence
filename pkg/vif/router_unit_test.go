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

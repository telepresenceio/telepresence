package driver

import (
	"net/netip"
	"testing"

	"github.com/telepresenceio/telepresence/rpc/v2/teleroute"
)

func mustPrefix(t *testing.T, s string) []byte {
	t.Helper()
	b, err := netip.MustParsePrefix(s).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustAddr(t *testing.T, s string) []byte {
	t.Helper()
	b, err := netip.MustParseAddr(s).MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestStaticRoutesFromResponse_dualStack verifies that on a mixed-family route set
// each route gets a next-hop of its own address family. A single shared next-hop
// (the previous behavior) gave IPv4 routes an IPv6 next-hop and broke the dual-stack
// connection.
func TestStaticRoutesFromResponse_dualStack(t *testing.T) {
	rsp := &teleroute.JoinResponse{
		Routes: [][]byte{
			mustPrefix(t, "10.96.0.0/16"),
			mustPrefix(t, "fd00:10:96::/112"),
			mustPrefix(t, "10.244.0.0/16"),
		},
		ViaIpV4: mustAddr(t, "172.30.0.2"),
		ViaIpV6: mustAddr(t, "fd00:0:0:246::2"),
	}
	srs, err := staticRoutesFromResponse(rsp)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"10.96.0.0/16":     "172.30.0.2",
		"fd00:10:96::/112": "fd00:0:0:246::2",
		"10.244.0.0/16":    "172.30.0.2",
	}
	if len(srs) != len(want) {
		t.Fatalf("got %d routes, want %d", len(srs), len(want))
	}
	for _, sr := range srs {
		w, ok := want[sr.Destination]
		if !ok {
			t.Errorf("unexpected route %s", sr.Destination)
			continue
		}
		if sr.NextHop != w {
			t.Errorf("route %s: next-hop %q, want %q", sr.Destination, sr.NextHop, w)
		}
		if sr.RouteType != 0 {
			t.Errorf("route %s: RouteType %d, want 0 (NEXTHOP)", sr.Destination, sr.RouteType)
		}
	}
}

// TestStaticRoutesFromResponse_missingFamilyVia verifies that a route whose family has
// no next-hop is emitted as a connected route rather than getting a wrong-family hop.
func TestStaticRoutesFromResponse_missingFamilyVia(t *testing.T) {
	rsp := &teleroute.JoinResponse{
		Routes:  [][]byte{mustPrefix(t, "fd00:10:96::/112")},
		ViaIpV4: mustAddr(t, "172.30.0.2"),
	}
	srs, err := staticRoutesFromResponse(rsp)
	if err != nil {
		t.Fatal(err)
	}
	if len(srs) != 1 {
		t.Fatalf("got %d routes, want 1", len(srs))
	}
	if srs[0].NextHop != "" || srs[0].RouteType != 1 {
		t.Errorf("IPv6 route without IPv6 via: NextHop %q RouteType %d, want \"\" and 1",
			srs[0].NextHop, srs[0].RouteType)
	}
}

// TestStaticRoutesFromResponse_legacyVia verifies backward compatibility: when an
// older daemon sends only the deprecated single via (and no per-family fields),
// routes fall back to it. Such daemons only ever produced single-family route sets.
func TestStaticRoutesFromResponse_legacyVia(t *testing.T) {
	rsp := &teleroute.JoinResponse{
		Routes: [][]byte{
			mustPrefix(t, "10.96.0.0/16"),
			mustPrefix(t, "10.244.0.0/16"),
		},
		Via: mustAddr(t, "172.30.0.2"),
	}
	srs, err := staticRoutesFromResponse(rsp)
	if err != nil {
		t.Fatal(err)
	}
	for _, sr := range srs {
		if sr.NextHop != "172.30.0.2" || sr.RouteType != 0 {
			t.Errorf("route %s: NextHop %q RouteType %d, want 172.30.0.2 / 0",
				sr.Destination, sr.NextHop, sr.RouteType)
		}
	}
}

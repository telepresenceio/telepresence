package vip

import (
	"net/netip"
	"testing"
)

func Test_Generators_familyDispatch(t *testing.T) {
	v4 := netip.MustParsePrefix("246.246.0.0/16")
	v6 := netip.MustParsePrefix("fd00:0:0:246::/64")
	g := NewGenerators(v4, v6)

	// Without EnsureFamily, no generator exists for either family.
	if _, err := g.Next(netip.MustParseAddr("10.0.0.1")); err == nil {
		t.Fatal("expected error for IPv4 before EnsureFamily")
	}
	if _, err := g.Next(netip.MustParseAddr("fd00:10:96::1")); err == nil {
		t.Fatal("expected error for IPv6 before EnsureFamily")
	}

	g.EnsureFamily(netip.MustParseAddr("10.0.0.1"))
	g.EnsureFamily(netip.MustParseAddr("fd00:10:96::1"))

	got4, err := g.Next(netip.MustParseAddr("10.0.0.1"))
	if err != nil {
		t.Fatalf("IPv4 Next: %v", err)
	}
	if !got4.Is4() || !v4.Contains(got4) {
		t.Errorf("IPv4 destination got %s, want an address in %s", got4, v4)
	}

	got6, err := g.Next(netip.MustParseAddr("fd00:10:96::1"))
	if err != nil {
		t.Fatalf("IPv6 Next: %v", err)
	}
	if got6.Is4() || !v6.Contains(got6) {
		t.Errorf("IPv6 destination got %s, want an address in %s", got6, v6)
	}

	subnets := g.Subnets()
	if len(subnets) != 2 {
		t.Fatalf("Subnets() = %v, want both families", subnets)
	}
}

func Test_Generators_ensureFamilyIdempotent(t *testing.T) {
	g := NewGenerators(netip.MustParsePrefix("246.246.0.0/16"), netip.MustParsePrefix("fd00:0:0:246::/64"))
	addr := netip.MustParseAddr("10.0.0.1")

	g.EnsureFamily(addr)
	first, err := g.Next(addr)
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}

	// A second EnsureFamily must not reset the generator.
	g.EnsureFamily(addr)
	second, err := g.Next(addr)
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if first == second {
		t.Errorf("EnsureFamily reset the generator: both calls returned %s", first)
	}
}

func Test_Generators_onlyRequestedFamily(t *testing.T) {
	g := NewGenerators(netip.MustParsePrefix("246.246.0.0/16"), netip.MustParsePrefix("fd00:0:0:246::/64"))
	g.EnsureFamily(netip.MustParseAddr("10.0.0.1"))

	// Only the IPv4 family was ensured, so the IPv6 range must not be routed.
	subnets := g.Subnets()
	if len(subnets) != 1 || !subnets[0].Addr().Is4() {
		t.Fatalf("Subnets() = %v, want only the IPv4 subnet", subnets)
	}
}

// Test_NewGenerator_allocatesLowerHalf verifies that a generator only hands out
// addresses from the lower half of its subnet, leaving the upper half for the
// device's owned source addresses so the two can never collide (Finding 3).
func Test_NewGenerator_allocatesLowerHalf(t *testing.T) {
	for _, tc := range []struct{ subnet, lower string }{
		{"10.0.0.0/24", "10.0.0.0/25"},
		{"fd00:0:0:246::/64", "fd00:0:0:246::/65"},
	} {
		t.Run(tc.subnet, func(t *testing.T) {
			g := NewGenerator(netip.MustParsePrefix(tc.subnet))
			lower := netip.MustParsePrefix(tc.lower)
			n := 0
			for {
				ip, err := g.Next()
				if err != nil {
					break // exhausted
				}
				if !lower.Contains(ip) {
					t.Fatalf("allocated %s outside the lower half %s", ip, lower)
				}
				n++
				if n > 200 {
					break // IPv6 lower half is huge; a sample is enough
				}
			}
			if n == 0 {
				t.Fatal("generator allocated nothing")
			}
			if g.Subnet().String() != tc.subnet {
				t.Errorf("Subnet() = %s, want the full subnet %s", g.Subnet(), tc.subnet)
			}
		})
	}
}

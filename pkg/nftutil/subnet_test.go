//go:build linux

package nftutil

import (
	"net/netip"
	"testing"
)

func TestLastAddr(t *testing.T) {
	tests := []struct {
		prefix string
		want   string
	}{
		{"10.0.0.0/24", "10.0.0.255"},
		{"10.0.0.0/32", "10.0.0.0"},
		{"10.96.0.0/12", "10.111.255.255"},
		{"0.0.0.0/0", "255.255.255.255"},
		{"fd00::/16", "fd00:ffff:ffff:ffff:ffff:ffff:ffff:ffff"},
		{"::/0", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"},
	}
	for _, tt := range tests {
		t.Run(tt.prefix, func(t *testing.T) {
			got := lastAddr(netip.MustParsePrefix(tt.prefix))
			want := netip.MustParseAddr(tt.want)
			if got != want {
				t.Errorf("lastAddr(%s) = %s, want %s", tt.prefix, got, want)
			}
		})
	}
}

func TestIntervalElementsSingleSubnet(t *testing.T) {
	elems, err := IntervalElements([]netip.Prefix{netip.MustParsePrefix("10.96.0.0/12")})
	if err != nil {
		t.Fatalf("IntervalElements() error = %v", err)
	}
	if len(elems) != 2 {
		t.Fatalf("len(elems) = %d, want 2 (start + end)", len(elems))
	}
	start, end := elems[0], elems[1]
	if start.IntervalEnd {
		t.Error("elems[0].IntervalEnd = true, want false (range start)")
	}
	if !end.IntervalEnd {
		t.Error("elems[1].IntervalEnd = false, want true (range end)")
	}
	wantStart := netip.MustParseAddr("10.96.0.0")
	wantEnd := netip.MustParseAddr("10.112.0.0") // 10.111.255.255 + 1
	if got, _ := netip.AddrFromSlice(start.Key); got != wantStart {
		t.Errorf("start key = %s, want %s", got, wantStart)
	}
	if got, _ := netip.AddrFromSlice(end.Key); got != wantEnd {
		t.Errorf("end key = %s, want %s", got, wantEnd)
	}
}

func TestIntervalElementsFullRangeHasNoEndMarker(t *testing.T) {
	elems, err := IntervalElements([]netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")})
	if err != nil {
		t.Fatalf("IntervalElements() error = %v", err)
	}
	// The range covers the entire address space, so there is no "address
	// immediately after the last one" to mark as the interval's end.
	if len(elems) != 1 {
		t.Fatalf("len(elems) = %d, want 1 (start only)", len(elems))
	}
	if elems[0].IntervalEnd {
		t.Error("elems[0].IntervalEnd = true, want false")
	}
}

func TestIntervalElementsAreSorted(t *testing.T) {
	elems, err := IntervalElements([]netip.Prefix{
		netip.MustParsePrefix("10.2.0.0/16"),
		netip.MustParsePrefix("10.0.0.0/16"),
	})
	if err != nil {
		t.Fatalf("IntervalElements() error = %v", err)
	}
	if len(elems) != 4 {
		t.Fatalf("len(elems) = %d, want 4", len(elems))
	}
	var prev netip.Addr
	for i, e := range elems {
		addr, ok := netip.AddrFromSlice(e.Key)
		if !ok {
			t.Fatalf("elems[%d].Key is not a valid address: %x", i, e.Key)
		}
		if i > 0 && !prev.Less(addr) {
			t.Errorf("elems not strictly sorted: %s then %s", prev, addr)
		}
		prev = addr
	}
}

func TestIntervalElementsRejectsInvalidPrefix(t *testing.T) {
	if _, err := IntervalElements([]netip.Prefix{{}}); err == nil {
		t.Fatal("IntervalElements() error = nil, want error for invalid prefix")
	}
}

// TestIntervalElementsCoalesces verifies that overlapping and adjacent subnets
// are merged into a single interval, so the kernel is never asked to insert
// overlapping intervals (which fails the whole atomic batch). Disjoint subnets
// stay separate.
func TestIntervalElementsCoalesces(t *testing.T) {
	// boundary is the [start, end-marker] pair a single interval reduces to.
	type boundary struct{ start, end string }
	collect := func(t *testing.T, prefixes ...string) []boundary {
		t.Helper()
		ps := make([]netip.Prefix, len(prefixes))
		for i, p := range prefixes {
			ps[i] = netip.MustParsePrefix(p)
		}
		elems, err := IntervalElements(ps)
		if err != nil {
			t.Fatalf("IntervalElements() error = %v", err)
		}
		var out []boundary
		for i := 0; i < len(elems); i += 2 {
			start, _ := netip.AddrFromSlice(elems[i].Key)
			if elems[i].IntervalEnd {
				t.Fatalf("elems[%d] unexpectedly flagged IntervalEnd", i)
			}
			end, _ := netip.AddrFromSlice(elems[i+1].Key)
			if !elems[i+1].IntervalEnd {
				t.Fatalf("elems[%d] not flagged IntervalEnd", i+1)
			}
			out = append(out, boundary{start.String(), end.String()})
		}
		return out
	}

	tests := []struct {
		name     string
		prefixes []string
		want     []boundary
	}{
		{
			name:     "nested same start",
			prefixes: []string{"10.96.0.0/16", "10.96.0.0/12"},
			want:     []boundary{{"10.96.0.0", "10.112.0.0"}},
		},
		{
			name:     "nested different start",
			prefixes: []string{"10.96.0.0/12", "10.100.0.0/16"},
			want:     []boundary{{"10.96.0.0", "10.112.0.0"}},
		},
		{
			name:     "adjacent",
			prefixes: []string{"10.96.0.0/17", "10.96.128.0/17"},
			want:     []boundary{{"10.96.0.0", "10.97.0.0"}},
		},
		{
			name:     "exact duplicate",
			prefixes: []string{"10.96.0.0/16", "10.96.0.0/16"},
			want:     []boundary{{"10.96.0.0", "10.97.0.0"}},
		},
		{
			name:     "disjoint stays separate",
			prefixes: []string{"10.96.0.0/16", "10.100.0.0/16"},
			want:     []boundary{{"10.96.0.0", "10.97.0.0"}, {"10.100.0.0", "10.101.0.0"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collect(t, tt.prefixes...)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d intervals %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("interval %d = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

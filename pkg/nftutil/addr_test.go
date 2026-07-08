//go:build linux

package nftutil

import (
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
)

func TestAddrLayout(t *testing.T) {
	tests := []struct {
		name       string
		family     nftables.TableFamily
		wantOffset uint32
		wantLen    uint32
	}{
		{"IPv4", nftables.TableFamilyIPv4, 16, 4},
		{"IPv6", nftables.TableFamilyIPv6, 24, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			off, ln := AddrLayout(tt.family)
			if off != tt.wantOffset || ln != tt.wantLen {
				t.Errorf("AddrLayout(%v) = %d/%d, want %d/%d", tt.family, off, ln, tt.wantOffset, tt.wantLen)
			}
		})
	}
}

func TestIntervalSet(t *testing.T) {
	tests := []struct {
		name       string
		family     nftables.TableFamily
		prefixes   []netip.Prefix
		wantKeyLen int
	}{
		{
			"IPv4",
			nftables.TableFamilyIPv4,
			[]netip.Prefix{netip.MustParsePrefix("10.96.0.0/12")},
			4,
		},
		{
			"IPv6",
			nftables.TableFamilyIPv6,
			[]netip.Prefix{netip.MustParsePrefix("fd00::/108")},
			16,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			table := &nftables.Table{Name: "telepresence", Family: tt.family}
			sd, err := IntervalSet(table, "test_set", tt.prefixes)
			if err != nil {
				t.Fatalf("IntervalSet() error = %v", err)
			}
			if sd.Set.Table != table {
				t.Error("set is not attached to the given table")
			}
			if sd.Set.Name != "test_set" {
				t.Errorf("set name = %q, want %q", sd.Set.Name, "test_set")
			}
			if !sd.Set.Interval {
				t.Error("interval set must set Interval")
			}
			if sd.Set.IsMap {
				t.Error("interval set must not be a map (membership only, no verdict data)")
			}
			wantKeyType := nftables.TypeIPAddr
			if tt.family == nftables.TableFamilyIPv6 {
				wantKeyType = nftables.TypeIP6Addr
			}
			if sd.Set.KeyType != wantKeyType {
				t.Errorf("KeyType = %v, want %v", sd.Set.KeyType, wantKeyType)
			}
			// A single subnet yields a start element and an end marker, in
			// order.
			if len(sd.Elements) != 2 {
				t.Fatalf("len(Elements) = %d, want 2 (start + end)", len(sd.Elements))
			}
			if sd.Elements[0].IntervalEnd {
				t.Error("Elements[0].IntervalEnd = true, want false (range start)")
			}
			if !sd.Elements[1].IntervalEnd {
				t.Error("Elements[1].IntervalEnd = false, want true (range end)")
			}
			for _, e := range sd.Elements {
				if len(e.Key) != tt.wantKeyLen {
					t.Errorf("element key length = %d, want %d", len(e.Key), tt.wantKeyLen)
				}
			}
		})
	}
}

func TestMatchDaddrInSet(t *testing.T) {
	table := &nftables.Table{Name: "telepresence", Family: nftables.TableFamilyIPv4}
	set := &nftables.Set{Table: table, Name: "test_set", ID: 42}

	for _, invert := range []bool{false, true} {
		exprs := MatchDaddrInSet(16, 4, set, invert)
		if len(exprs) != 2 {
			t.Fatalf("MatchDaddrInSet(invert=%v) returned %d expressions, want 2", invert, len(exprs))
		}
		p, ok := exprs[0].(*expr.Payload)
		if !ok {
			t.Fatalf("exprs[0] is %T, want *expr.Payload", exprs[0])
		}
		if p.DestRegister != 1 || p.Base != expr.PayloadBaseNetworkHeader || p.Offset != 16 || p.Len != 4 {
			t.Errorf("Payload = %+v, want DestRegister=1 Base=NetworkHeader Offset=16 Len=4", p)
		}
		l, ok := exprs[1].(*expr.Lookup)
		if !ok {
			t.Fatalf("exprs[1] is %T, want *expr.Lookup", exprs[1])
		}
		if l.SourceRegister != 1 || l.SetName != set.Name || l.SetID != set.ID || l.Invert != invert {
			t.Errorf("Lookup = %+v, want SourceRegister=1 SetName=%q SetID=%d Invert=%v", l, set.Name, set.ID, invert)
		}
	}
}

func TestIntervalSetRejectsInvalidPrefix(t *testing.T) {
	table := &nftables.Table{Name: "telepresence", Family: nftables.TableFamilyIPv4}
	if _, err := IntervalSet(table, "test_set", []netip.Prefix{{}}); err == nil {
		t.Fatal("IntervalSet() error = nil, want error for invalid prefix")
	}
}

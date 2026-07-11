//go:build linux

package routenft

import (
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
)

func basicConfig() Config {
	return Config{
		Family: nftables.TableFamilyIPv4,
		CIDRs:  []netip.Prefix{netip.MustParsePrefix("10.96.0.0/12")},
	}
}

func TestBuildValidatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"unsupported family", func(c *Config) { c.Family = nftables.TableFamilyARP }, true},
		{"no CIDRs", func(c *Config) { c.CIDRs = nil }, true},
		{"invalid CIDR", func(c *Config) { c.CIDRs = []netip.Prefix{{}} }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := basicConfig()
			tt.mutate(&cfg)
			_, err := Build(cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildTableFamily(t *testing.T) {
	tests := []struct {
		name       string
		family     nftables.TableFamily
		cidr       string
		wantKeyLen int
	}{
		{"IPv4", nftables.TableFamilyIPv4, "10.96.0.0/12", 4},
		{"IPv6", nftables.TableFamilyIPv6, "fd00::/108", 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Family: tt.family, CIDRs: []netip.Prefix{netip.MustParsePrefix(tt.cidr)}}
			rs, err := Build(cfg)
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			if rs.Table.Family != tt.family {
				t.Errorf("Table.Family = %v, want %v", rs.Table.Family, tt.family)
			}
			if rs.Table.Name != TableName {
				t.Errorf("Table.Name = %q, want %q", rs.Table.Name, TableName)
			}
			for _, e := range rs.ServiceCIDRs.Elements {
				if len(e.Key) != tt.wantKeyLen {
					t.Errorf("set element key length = %d, want %d", len(e.Key), tt.wantKeyLen)
				}
			}
		})
	}
}

func TestBuildForwardChainHookAndPriority(t *testing.T) {
	rs, err := Build(basicConfig())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if rs.Forward.Type != nftables.ChainTypeFilter {
		t.Errorf("Forward.Type = %v, want filter", rs.Forward.Type)
	}
	if *rs.Forward.Hooknum != *nftables.ChainHookForward {
		t.Errorf("Forward.Hooknum = %v, want forward", *rs.Forward.Hooknum)
	}
	if *rs.Forward.Priority != *nftables.ChainPriorityFilter {
		t.Errorf("Forward.Priority = %v, want filter (0)", *rs.Forward.Priority)
	}
}

func TestBuildServiceCIDRSet(t *testing.T) {
	cfg := Config{
		Family: nftables.TableFamilyIPv4,
		CIDRs: []netip.Prefix{
			netip.MustParsePrefix("10.96.0.0/12"),
			netip.MustParsePrefix("172.20.0.0/16"),
		},
	}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if rs.ServiceCIDRs.Set.Name != setServiceCIDRs {
		t.Errorf("set name = %q, want %q", rs.ServiceCIDRs.Set.Name, setServiceCIDRs)
	}
	if !rs.ServiceCIDRs.Set.Interval {
		t.Error("service CIDR set must be an interval set")
	}
	if rs.ServiceCIDRs.Set.IsMap {
		t.Error("service CIDR set must not be a map (membership only, no verdict data)")
	}
	if rs.ServiceCIDRs.Set.KeyType != nftables.TypeIPAddr {
		t.Errorf("set key type = %v, want ipv4_addr", rs.ServiceCIDRs.Set.KeyType)
	}
	// Two /12 and /16 subnets each yield a start + end boundary.
	if len(rs.ServiceCIDRs.Elements) != 4 {
		t.Errorf("len(Elements) = %d, want 4", len(rs.ServiceCIDRs.Elements))
	}
	if len(rs.Sets) != 1 || rs.Sets[0] != rs.ServiceCIDRs {
		t.Errorf("Sets = %+v, want [ServiceCIDRs]", rs.Sets)
	}
}

func TestBuildSingleDropRule(t *testing.T) {
	rs, err := Build(basicConfig())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(rs.Rules) != 1 {
		t.Fatalf("len(Rules) = %d, want 1", len(rs.Rules))
	}
	r := rs.Rules[0]
	if r.Chain != rs.Forward {
		t.Error("rule is not attached to the forward chain")
	}
	if r.Table != rs.Table {
		t.Error("rule is not attached to the ruleset's table")
	}

	var sawLookup, sawDrop bool
	var lookupInvert bool
	for _, e := range r.Exprs {
		switch v := e.(type) {
		case *expr.Lookup:
			sawLookup = true
			lookupInvert = v.Invert
			if v.SetName != rs.ServiceCIDRs.Set.Name {
				t.Errorf("lookup set name = %q, want %q", v.SetName, rs.ServiceCIDRs.Set.Name)
			}
		case *expr.Verdict:
			sawDrop = v.Kind == expr.VerdictDrop
		}
	}
	if !sawLookup {
		t.Error("rule does not look up the service-CIDR set")
	}
	if lookupInvert {
		t.Error("lookup must not be inverted: we want to match daddrs IN the set")
	}
	if !sawDrop {
		t.Error("rule does not end in a drop verdict")
	}
}

func TestBuildPayloadOffsetsPerFamily(t *testing.T) {
	tests := []struct {
		name       string
		family     nftables.TableFamily
		cidr       string
		wantOffset uint32
		wantLen    uint32
	}{
		{"IPv4", nftables.TableFamilyIPv4, "10.96.0.0/12", 16, 4},
		{"IPv6", nftables.TableFamilyIPv6, "fd00::/108", 24, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Family: tt.family, CIDRs: []netip.Prefix{netip.MustParsePrefix(tt.cidr)}}
			rs, err := Build(cfg)
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}
			for _, e := range rs.Rules[0].Exprs {
				p, ok := e.(*expr.Payload)
				if !ok {
					continue
				}
				if p.Base != expr.PayloadBaseNetworkHeader {
					t.Errorf("payload base = %v, want network header", p.Base)
				}
				if p.Offset != tt.wantOffset || p.Len != tt.wantLen {
					t.Errorf("payload offset/len = %d/%d, want %d/%d", p.Offset, p.Len, tt.wantOffset, tt.wantLen)
				}
			}
		})
	}
}

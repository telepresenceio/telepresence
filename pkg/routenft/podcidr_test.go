//go:build linux

package routenft

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"

	"github.com/telepresenceio/telepresence/v2/pkg/nftutil"
)

func podConfig() Config {
	return Config{
		Family:       nftables.TableFamilyIPv4,
		ServiceCIDRs: []netip.Prefix{netip.MustParsePrefix("10.96.0.0/12")},
		PodCIDRs:     []netip.Prefix{netip.MustParsePrefix("10.244.0.0/24")},
		Uplinks:      []string{"eth0"},
	}
}

func TestBuildValidatesPodConfig(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"pod CIDRs only", func(c *Config) { c.ServiceCIDRs = nil }, false},
		{"pod CIDRs without uplink", func(c *Config) { c.Uplinks = nil }, true},
		{"uplink without pod CIDRs", func(c *Config) { c.PodCIDRs = nil }, false},
		{"invalid pod CIDR", func(c *Config) { c.PodCIDRs = []netip.Prefix{{}} }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := podConfig()
			tt.mutate(&cfg)
			_, err := Build(cfg)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildPodCIDRSet(t *testing.T) {
	rs, err := Build(podConfig())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if rs.PodCIDRs == nil {
		t.Fatal("PodCIDRs set not built")
	}
	if rs.PodCIDRs.Set.Name != setPodCIDRs {
		t.Errorf("set name = %q, want %q", rs.PodCIDRs.Set.Name, setPodCIDRs)
	}
	if !rs.PodCIDRs.Set.Interval {
		t.Error("pod CIDR set must be an interval set")
	}
	if len(rs.PodCIDRs.Elements) != 2 {
		t.Errorf("len(Elements) = %d, want 2", len(rs.PodCIDRs.Elements))
	}
	if len(rs.Sets) != 2 || rs.Sets[0] != rs.ServiceCIDRs || rs.Sets[1] != rs.PodCIDRs {
		t.Errorf("Sets = %+v, want [ServiceCIDRs PodCIDRs]", rs.Sets)
	}
}

func TestBuildPodCIDRRulePerUplink(t *testing.T) {
	cfg := podConfig()
	cfg.Uplinks = []string{"eth0", "eth1"}
	rs, err := Build(cfg)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	// One service CIDR rule followed by one pod CIDR rule per uplink.
	if len(rs.Rules) != 3 {
		t.Fatalf("len(Rules) = %d, want 3", len(rs.Rules))
	}
	for i, uplink := range cfg.Uplinks {
		r := rs.Rules[i+1]
		if r.Chain != rs.Forward {
			t.Errorf("rule %d is not attached to the forward chain", i+1)
		}
		var sawOif, sawLookup, sawDrop bool
		for j, e := range r.Exprs {
			switch v := e.(type) {
			case *expr.Meta:
				if v.Key != expr.MetaKeyOIFNAME {
					t.Errorf("rule %d: meta key = %v, want oifname", i+1, v.Key)
				}
				cmp, ok := r.Exprs[j+1].(*expr.Cmp)
				if !ok {
					t.Fatalf("rule %d: oifname load is not followed by a compare", i+1)
				}
				if !bytes.Equal(cmp.Data, nftutil.IfnameBytes(uplink)) {
					t.Errorf("rule %d: oifname = %q, want %q", i+1, bytes.TrimRight(cmp.Data, "\x00"), uplink)
				}
				sawOif = true
			case *expr.Lookup:
				sawLookup = true
				if v.SetName != setPodCIDRs {
					t.Errorf("rule %d: lookup set = %q, want %q", i+1, v.SetName, setPodCIDRs)
				}
				if v.Invert {
					t.Errorf("rule %d: lookup must not be inverted", i+1)
				}
			case *expr.Verdict:
				sawDrop = v.Kind == expr.VerdictDrop
			}
		}
		if !sawOif {
			t.Errorf("rule %d does not match the output interface", i+1)
		}
		if !sawLookup {
			t.Errorf("rule %d does not look up the pod CIDR set", i+1)
		}
		if !sawDrop {
			t.Errorf("rule %d does not end in a drop verdict", i+1)
		}
	}
}

func TestBuildServiceCIDRRuleHasNoInterfaceMatch(t *testing.T) {
	rs, err := Build(podConfig())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, e := range rs.Rules[0].Exprs {
		if _, ok := e.(*expr.Meta); ok {
			t.Fatal("service CIDR rule must drop regardless of output interface")
		}
	}
}

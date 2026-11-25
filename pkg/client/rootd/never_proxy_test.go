package rootd

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Nesting and also-proxy overrides are pure prefix logic that a cluster cannot
// be steered into reliably; the regression suite covers the plain refusal.
func TestNeverProxyCovering(t *testing.T) {
	pfx := func(ss ...string) (ps []netip.Prefix) {
		for _, s := range ss {
			ps = append(ps, netip.MustParsePrefix(s))
		}
		return ps
	}
	tests := []struct {
		name  string
		ip    string
		nvp   []netip.Prefix
		also  []netip.Prefix
		want  string
		found bool
	}{
		{"none", "10.1.2.3", nil, nil, "", false},
		{"outside", "10.1.2.3", pfx("192.168.0.0/16"), nil, "", false},
		{"host route", "10.1.2.3", pfx("10.1.2.3/32"), nil, "10.1.2.3/32", true},
		{"narrowest wins", "10.1.2.3", pfx("10.0.0.0/8", "10.1.0.0/16"), nil, "10.1.0.0/16", true},
		{"also-proxy inside overrides", "10.1.2.3", pfx("10.0.0.0/8"), pfx("10.1.0.0/16"), "", false},
		{"also-proxy elsewhere", "10.1.2.3", pfx("10.0.0.0/8"), pfx("10.9.0.0/16"), "10.0.0.0/8", true},
		{"also-proxy wider than never-proxy", "10.1.2.3", pfx("10.1.2.0/24"), pfx("10.0.0.0/8"), "10.1.2.0/24", true},
		{"ipv6", "fd00::5", pfx("fd00::/64"), nil, "fd00::/64", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := neverProxyCovering(netip.MustParseAddr(tt.ip), tt.nvp, tt.also)
			assert.Equal(t, tt.found, found)
			if tt.found {
				assert.Equal(t, tt.want, got.String())
			}
		})
	}
}

package teleroute

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOverlapsAny(t *testing.T) {
	cidrs := []netip.Prefix{
		netip.MustParsePrefix("172.20.0.0/16"),
		netip.MustParsePrefix("10.100.0.0/16"),
	}

	tests := []struct {
		name   string
		subnet string
		want   bool
	}{
		{"exact match", "172.20.0.0/16", true},
		{"contained in", "172.20.1.0/24", true},
		{"contains", "172.16.0.0/12", true},
		{"partial overlap", "172.20.0.0/15", true},
		{"no overlap", "192.168.0.0/16", false},
		{"adjacent no overlap", "172.21.0.0/16", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := overlapsAny(netip.MustParsePrefix(tt.subnet), cidrs)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNextPrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"basic /16", "172.16.0.0/16", "172.17.0.0/16"},
		{"basic /24", "10.0.0.0/24", "10.0.1.0/24"},
		{"basic /20", "172.16.0.0/20", "172.16.16.0/20"},
		{"wrap byte boundary", "10.0.255.0/24", "10.1.0.0/24"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nextPrefix(netip.MustParsePrefix(tt.in))
			assert.Equal(t, tt.want, got.String())
		})
	}

	t.Run("overflow", func(t *testing.T) {
		got := nextPrefix(netip.MustParsePrefix("255.255.0.0/16"))
		assert.False(t, got.IsValid())
	})
}

func TestFindFreeSubnet(t *testing.T) {
	t.Run("skips conflicting /16", func(t *testing.T) {
		// Block 172.16.0.0/16, expect 172.17.0.0/16.
		avoid := []netip.Prefix{
			netip.MustParsePrefix("172.16.0.0/16"),
		}
		got, err := findFreeSubnet(avoid)
		require.NoError(t, err)
		assert.Equal(t, "172.17.0.0/16", got.String())
	})

	t.Run("skips multiple conflicting /16s", func(t *testing.T) {
		// Block all of 172.16-19, expect 172.20.0.0/16.
		avoid := []netip.Prefix{
			netip.MustParsePrefix("172.16.0.0/16"),
			netip.MustParsePrefix("172.17.0.0/16"),
			netip.MustParsePrefix("172.18.0.0/16"),
			netip.MustParsePrefix("172.19.0.0/16"),
		}
		got, err := findFreeSubnet(avoid)
		require.NoError(t, err)
		assert.Equal(t, "172.20.0.0/16", got.String())
	})

	t.Run("skips EKS default CIDR", func(t *testing.T) {
		// EKS uses 172.20.0.0/16. Block first 4 plus EKS, expect 172.21.0.0/16.
		avoid := []netip.Prefix{
			netip.MustParsePrefix("172.16.0.0/16"),
			netip.MustParsePrefix("172.17.0.0/16"),
			netip.MustParsePrefix("172.18.0.0/16"),
			netip.MustParsePrefix("172.19.0.0/16"),
			netip.MustParsePrefix("172.20.0.0/16"),
		}
		got, err := findFreeSubnet(avoid)
		require.NoError(t, err)
		assert.Equal(t, "172.21.0.0/16", got.String())
	})

	t.Run("no conflict returns first candidate", func(t *testing.T) {
		got, err := findFreeSubnet(nil)
		require.NoError(t, err)
		assert.Equal(t, "172.16.0.0/16", got.String())
	})

	t.Run("falls back to /20 when all /16s blocked", func(t *testing.T) {
		// Block entire 172.16.0.0/12 range. All /16 candidates will conflict,
		// so it should fall back to 10.0.0.0/20.
		avoid := []netip.Prefix{
			netip.MustParsePrefix("172.16.0.0/12"),
		}
		got, err := findFreeSubnet(avoid)
		require.NoError(t, err)
		assert.Equal(t, "10.0.0.0/20", got.String())
	})
}

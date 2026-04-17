package rootd

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsAlsoProxyDestination(t *testing.T) {
	s := &session{
		alsoProxySubnets: []netip.Prefix{
			netip.MustParsePrefix("240.240.0.0/16"),
			netip.MustParsePrefix("fd00::/8"),
		},
	}

	require.True(t, s.isAlsoProxyDestination(netip.MustParseAddr("240.240.0.33")))
	require.True(t, s.isAlsoProxyDestination(netip.MustParseAddr("fd00::1")))
	require.False(t, s.isAlsoProxyDestination(netip.MustParseAddr("10.128.0.10")))
	require.False(t, s.isAlsoProxyDestination(netip.MustParseAddr("2001:db8::1")))
}

//go:build linux

package dns

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseResolveCtlDNS(t *testing.T) {
	out := []byte(`
Global: 127.0.0.1:5300
Link 2 (ens5): 172.31.0.2 2001:db8::53%ens5
Link 3 (wg0): 10.0.0.1#corp.example
Link 4 (eth1): [2001:db8::1]:53
`)
	got := parseResolveCtlDNS(out)

	require.Equal(t, []netip.AddrPort{
		netip.MustParseAddrPort("10.0.0.1:53"),
		netip.MustParseAddrPort("127.0.0.1:5300"),
		netip.MustParseAddrPort("172.31.0.2:53"),
		netip.MustParseAddrPort("[2001:db8::1]:53"),
		netip.MustParseAddrPort("[2001:db8::53]:53"),
	}, got)
}

func TestLinkDomainsIncludesNamespaceQualifiedServiceRoutes(t *testing.T) {
	paths := linkDomains(
		[]string{"tel2-search"},
		map[string]struct{}{"app": {}, "other": {}, "svc": {}},
		[]string{".internal"},
		"cluster.local.",
	)

	require.ElementsMatch(t, []string{
		"tel2-search",
		"~app",
		"~app.svc.cluster.local",
		"~other",
		"~other.svc.cluster.local",
		"~svc",
		"~internal.",
		"~cluster.local.",
	}, paths)
}

package rootd

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func binAddrPort(t *testing.T, s string) []byte {
	t.Helper()
	b, err := netip.MustParseAddrPort(s).MarshalBinary()
	require.NoError(t, err)
	return b
}

func TestShortcutTableMatchesServiceAddress(t *testing.T) {
	rq := require.New(t)
	tbl := newShortcutTable(context.Background(), []*rpc.InterceptShortcut{
		{
			Namespace:     "default",
			Workload:      "echo",
			Protocol:      "TCP",
			ServiceAddrs:  [][]byte{binAddrPort(t, "10.96.0.10:80")},
			ContainerPort: 8080,
			Target:        binAddrPort(t, "127.0.0.1:8080"),
		},
	})

	lt, ok := tbl.target(types.ProtoTCP, netip.MustParseAddrPort("10.96.0.10:80"), nil)
	rq.True(ok)
	rq.Equal(netip.MustParseAddrPort("127.0.0.1:8080"), lt)

	// Wrong port, wrong address, and wrong protocol must all miss.
	_, ok = tbl.target(types.ProtoTCP, netip.MustParseAddrPort("10.96.0.10:81"), nil)
	rq.False(ok)
	_, ok = tbl.target(types.ProtoTCP, netip.MustParseAddrPort("10.96.0.11:80"), nil)
	rq.False(ok)
	_, ok = tbl.target(types.ProtoUDP, netip.MustParseAddrPort("10.96.0.10:80"), nil)
	rq.False(ok)
}

func TestShortcutTableMatchesWorkloadPod(t *testing.T) {
	rq := require.New(t)
	tbl := newShortcutTable(context.Background(), []*rpc.InterceptShortcut{
		{
			Namespace:     "default",
			Workload:      "echo",
			Protocol:      "TCP",
			ContainerPort: 8080,
			Target:        binAddrPort(t, "127.0.0.1:8080"),
		},
	})

	podWorkload := func(ip netip.Addr) (string, string, bool) {
		if ip == netip.MustParseAddr("10.244.0.7") {
			return "echo", "default", true
		}
		if ip == netip.MustParseAddr("10.244.0.8") {
			return "other", "default", true
		}
		return "", "", false
	}

	lt, ok := tbl.target(types.ProtoTCP, netip.MustParseAddrPort("10.244.0.7:8080"), podWorkload)
	rq.True(ok)
	rq.Equal(netip.MustParseAddrPort("127.0.0.1:8080"), lt)

	// Other workload, other container port, unknown pod, and absent pod lookup must miss.
	_, ok = tbl.target(types.ProtoTCP, netip.MustParseAddrPort("10.244.0.8:8080"), podWorkload)
	rq.False(ok)
	_, ok = tbl.target(types.ProtoTCP, netip.MustParseAddrPort("10.244.0.7:8081"), podWorkload)
	rq.False(ok)
	_, ok = tbl.target(types.ProtoTCP, netip.MustParseAddrPort("10.244.0.9:8080"), podWorkload)
	rq.False(ok)
	_, ok = tbl.target(types.ProtoTCP, netip.MustParseAddrPort("10.244.0.7:8080"), nil)
	rq.False(ok)
}

func TestShortcutTableIsReplacedWholesale(t *testing.T) {
	rq := require.New(t)
	s := &session{}
	s.SetInterceptShortcuts(context.Background(), []*rpc.InterceptShortcut{
		{
			Namespace:    "default",
			Workload:     "echo",
			Protocol:     "TCP",
			ServiceAddrs: [][]byte{binAddrPort(t, "10.96.0.10:80")},
			Target:       binAddrPort(t, "127.0.0.1:8080"),
		},
	})
	_, ok := s.shortcutTarget(types.ProtoTCP, netip.MustParseAddrPort("10.96.0.10:80"))
	rq.True(ok)

	// An empty set clears all previous shortcuts.
	s.SetInterceptShortcuts(context.Background(), nil)
	_, ok = s.shortcutTarget(types.ProtoTCP, netip.MustParseAddrPort("10.96.0.10:80"))
	rq.False(ok)
}

func TestShortcutTableSkipsMalformedShortcuts(t *testing.T) {
	rq := require.New(t)
	good := &rpc.InterceptShortcut{
		Namespace:     "default",
		Workload:      "echo",
		Protocol:      "TCP",
		ServiceAddrs:  [][]byte{binAddrPort(t, "10.96.0.10:80")},
		ContainerPort: 8080,
		Target:        binAddrPort(t, "127.0.0.1:8080"),
	}
	tbl := newShortcutTable(context.Background(), []*rpc.InterceptShortcut{
		{Workload: "bad-proto", Protocol: "bogus", Target: binAddrPort(t, "127.0.0.1:8081")},
		{Workload: "bad-target", Protocol: "TCP", ContainerPort: 8081, Target: []byte{1, 2, 3}},
		{Workload: "bad-svc", Protocol: "TCP", ServiceAddrs: [][]byte{{4, 5}}, Target: binAddrPort(t, "127.0.0.1:8082")},
		good,
	})

	// The valid entry survives, the malformed ones are ignored.
	lt, ok := tbl.target(types.ProtoTCP, netip.MustParseAddrPort("10.96.0.10:80"), nil)
	rq.True(ok)
	rq.Equal(netip.MustParseAddrPort("127.0.0.1:8080"), lt)
	rq.Len(tbl.byAddr, 1)
	rq.Len(tbl.byWorkload, 1)
}

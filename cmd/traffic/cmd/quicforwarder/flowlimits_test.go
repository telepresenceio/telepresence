package quicforwarder

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/ipv4"

	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
)

func newLimitTable(t *testing.T) (*flowTable, uint16) {
	t.Helper()
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = front.Close() })
	backend, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Close() })
	ft := newFlowTable(ipv4.NewPacketConn(front), newMetrics())
	t.Cleanup(ft.closeAll)
	return ft, uint16(backend.LocalAddr().(*net.UDPAddr).Port)
}

var loopback = netip.MustParseAddr("127.0.0.1")

func TestFlowTable_TotalCap(t *testing.T) {
	ft, port := newLimitTable(t)
	ft.maxFlows = 10
	ft.maxFlowsPerIP = 0
	for i := range 30 {
		src := netip.AddrPortFrom(netip.AddrFrom4([4]byte{198, 51, 100, byte(i + 1)}), 4000)
		ft.CreateAndForward(context.Background(), src, loopback, port, [][]byte{[]byte("x")})
	}
	assert.Equal(t, 10, ft.count())
	assert.Equal(t, int64(20), ft.metrics.dropped[dropFlowCap].Load())

	// Releasing capacity lets new flows in again.
	ft.sweepIdle(context.Background(), -time.Second)
	assert.Equal(t, 0, ft.count())
	ft.CreateAndForward(context.Background(), netip.MustParseAddrPort("198.51.100.99:1"), loopback, port, [][]byte{[]byte("x")})
	assert.Equal(t, 1, ft.count())
}

func TestFlowTable_PerIPCap(t *testing.T) {
	ft, port := newLimitTable(t)
	ft.maxFlowsPerIP = 3
	for i := range 20 {
		src := netip.AddrPortFrom(netip.MustParseAddr("198.51.100.7"), uint16(5000+i))
		ft.CreateAndForward(context.Background(), src, loopback, port, [][]byte{[]byte("x")})
	}
	assert.Equal(t, 3, ft.count())

	other := netip.MustParseAddrPort("198.51.100.8:5000")
	ft.CreateAndForward(context.Background(), other, loopback, port, [][]byte{[]byte("x")})
	assert.Equal(t, 4, ft.count())
	_, ok := ft.lookup(other)
	assert.True(t, ok)
}

func TestFlowTable_HalfOpenExpiresEstablishedStays(t *testing.T) {
	ft, port := newLimitTable(t)
	ft.halfOpenAfter = 100 * time.Millisecond

	backend, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer backend.Close()
	bport := uint16(backend.LocalAddr().(*net.UDPAddr).Port)

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer client.Close()
	established := client.LocalAddr().(*net.UDPAddr).AddrPort()
	silent := netip.MustParseAddrPort("127.0.0.1:2")

	ctx := context.Background()
	ft.CreateAndForward(ctx, established, loopback, bport, [][]byte{[]byte("hi")})
	ft.CreateAndForward(ctx, silent, loopback, port, [][]byte{[]byte("hi")})
	require.Equal(t, 2, ft.count())

	buf := make([]byte, 64)
	require.NoError(t, backend.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, from, err := backend.ReadFromUDP(buf)
	require.NoError(t, err)
	_, err = backend.WriteToUDP([]byte("reply"), from)
	require.NoError(t, err)
	require.NoError(t, client.SetReadDeadline(time.Now().Add(2*time.Second)))
	_, err = client.Read(buf)
	require.NoError(t, err)

	time.Sleep(250 * time.Millisecond)
	// Client traffic does not keep a half-open flow alive.
	ft.Forward(ctx, silent, []byte("again"))
	ft.sweepIdle(ctx, time.Hour)
	assert.Equal(t, 1, ft.count())
	_, ok := ft.lookup(established)
	assert.True(t, ok)
	_, ok = ft.lookup(silent)
	assert.False(t, ok)
}

func TestFlowTable_HalfOpenFlowsAreCheap(t *testing.T) {
	ft, port := newLimitTable(t)
	ft.maxFlows = 0
	ft.maxFlowsPerIP = 0

	const n = 100
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := range n {
		src := netip.AddrPortFrom(loopback, uint16(10000+i))
		ft.CreateAndForward(context.Background(), src, loopback, port, [][]byte{[]byte("x")})
	}
	require.Equal(t, n, ft.count())
	time.Sleep(200 * time.Millisecond) // let the pumps start
	runtime.GC()
	runtime.ReadMemStats(&after)
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	// A full read batch is about 4 MiB per flow.
	assert.Less(t, grew, int64(n)*(256<<10), "heap grew by %d bytes for %d flows", grew, n)
}

func TestRoute_HandshakeCacheEntryLimit(t *testing.T) {
	packets := captureInitialDatagrams(t, quicfwd.ManagerSNI)
	require.GreaterOrEqual(t, len(packets), 2)

	allowlist := newFakeAllowlist()
	allowlist.setManager(netip.MustParseAddr("10.9.9.9"), 7778)
	r := NewRouter(allowlist, newFakeSink(), newMetrics())
	r.maxHandshakes = 5

	for i := range 50 {
		src := netip.MustParseAddrPort(fmt.Sprintf("192.0.2.%d:4000", i+1))
		r.Route(context.Background(), src, packets[0])
	}
	r.mu.Lock()
	size := len(r.handshakes)
	r.mu.Unlock()
	assert.Equal(t, 5, size)
	assert.Equal(t, int64(45), r.metrics.dropped[dropCapExceeded].Load())

	// Once entries expire, new handshakes are accepted again.
	now := time.Now().Add(handshakeTTL + time.Second)
	r.clock = func() time.Time { return now }
	r.Sweep(context.Background())
	r.Route(context.Background(), netip.MustParseAddrPort("192.0.2.200:4000"), packets[0])
	r.mu.Lock()
	size = len(r.handshakes)
	r.mu.Unlock()
	assert.Equal(t, 1, size)
}

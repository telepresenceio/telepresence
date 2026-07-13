package quicforwarder

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFlowTable_CreateAndForward_RoundTrip drives a real UDP "backend" socket through
// flowTable: CreateAndForward must deliver the buffered datagrams to the backend, and
// the backend's replies must come back out of the front socket addressed to the
// client's source address.
func TestFlowTable_CreateAndForward_RoundTrip(t *testing.T) {
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer front.Close()

	backend, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer backend.Close()

	backendPort := uint16(backend.LocalAddr().(*net.UDPAddr).Port)
	m := newMetrics()
	ft := newFlowTable(front, backendPort, m)

	// Simulate a "client" by using a second UDP socket whose address is what
	// the flow is keyed on -- but the flow table itself never touches this
	// socket; it only needs a src netip.AddrPort to route replies to, and
	// front.WriteToUDPAddrPort will actually deliver to whatever real address
	// that is.
	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer client.Close()
	src := client.LocalAddr().(*net.UDPAddr).AddrPort()

	ctx := context.Background()
	backendIP := netip.MustParseAddr("127.0.0.1")
	ft.CreateAndForward(ctx, src, backendIP, [][]byte{[]byte("hello-1"), []byte("hello-2")})

	buf := make([]byte, 1024)
	require.NoError(t, backend.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, from, err := backend.ReadFromUDP(buf)
	require.NoError(t, err)
	assert.Equal(t, "hello-1", string(buf[:n]))
	n, _, err = backend.ReadFromUDP(buf)
	require.NoError(t, err)
	assert.Equal(t, "hello-2", string(buf[:n]))

	// Reply from the backend must reach the client's socket, relayed through
	// front and addressed to src.
	_, err = backend.WriteToUDP([]byte("reply"), from)
	require.NoError(t, err)

	require.NoError(t, client.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, err = client.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "reply", string(buf[:n]))

	// A further datagram from the same source uses Forward, not
	// CreateAndForward, and must reach the same backend connection.
	ok := ft.Forward(ctx, src, []byte("hello-3"))
	assert.True(t, ok)
	n, err = backend.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "hello-3", string(buf[:n]))

	assert.Equal(t, 1, ft.count())
	ft.closeAll()
	assert.Equal(t, 0, ft.count())
}

func TestFlowTable_Forward_UnknownSourceReturnsFalse(t *testing.T) {
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer front.Close()

	ft := newFlowTable(front, 12345, newMetrics())
	ok := ft.Forward(context.Background(), netip.MustParseAddrPort("127.0.0.1:1"), []byte("x"))
	assert.False(t, ok)
}

func TestFlowTable_SweepIdle_ClosesStaleFlows(t *testing.T) {
	front, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer front.Close()

	backend, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer backend.Close()
	backendPort := uint16(backend.LocalAddr().(*net.UDPAddr).Port)

	ft := newFlowTable(front, backendPort, newMetrics())
	src := netip.MustParseAddrPort("127.0.0.1:54321")
	ft.CreateAndForward(context.Background(), src, netip.MustParseAddr("127.0.0.1"), [][]byte{[]byte("x")})
	require.Equal(t, 1, ft.count())

	// Not idle yet at a generous idle threshold.
	ft.sweepIdle(context.Background(), time.Hour)
	assert.Equal(t, 1, ft.count())

	// A zero (or negative) idle threshold means everything is "idle".
	ft.sweepIdle(context.Background(), -time.Second)
	assert.Equal(t, 0, ft.count())
}

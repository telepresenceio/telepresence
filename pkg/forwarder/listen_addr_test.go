package forwarder

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

var loopback = netip.AddrFrom4([4]byte{127, 0, 0, 1})

// TestListen_WithListenAddr proves that the WithListenAddr option restricts a
// TCP forwarder's listener to the given address.
func TestListen_WithListenAddr(t *testing.T) {
	ctx, cancel := context.WithTimeout(testutil.NewContext(t, false), 5*time.Second)
	defer cancel()

	f := NewTCP(0, tunnel.ClientToAgent, netip.AddrPortFrom(loopback, 1), WithListenAddr(loopback))
	listener, err := f.(interface {
		Listen(context.Context) (net.Listener, error)
	}).Listen(ctx)
	require.NoError(t, err)
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	require.True(t, addr.IP.IsLoopback(), "listener must be bound to the loopback address, got %s", addr)
}

// TestListen_DefaultRemainsWildcard proves that a forwarder without the
// WithListenAddr option still binds all available addresses.
func TestListen_DefaultRemainsWildcard(t *testing.T) {
	ctx, cancel := context.WithTimeout(testutil.NewContext(t, false), 5*time.Second)
	defer cancel()

	f := NewTCP(0, tunnel.ClientToAgent, netip.AddrPortFrom(loopback, 1))
	listener, err := f.(interface {
		Listen(context.Context) (net.Listener, error)
	}).Listen(ctx)
	require.NoError(t, err)
	defer listener.Close()

	addr := listener.Addr().(*net.TCPAddr)
	require.True(t, addr.IP.IsUnspecified(), "listener must be bound to all addresses, got %s", addr)
}

// TestServeUDP_WithListenAddr proves that the WithListenAddr option restricts
// a UDP forwarder's packet listener to the given address.
func TestServeUDP_WithListenAddr(t *testing.T) {
	ctx, cancel := context.WithTimeout(testutil.NewContext(t, false), 5*time.Second)
	defer cancel()

	target := udpEcho(t)
	targetAddr := target.LocalAddr().(*net.UDPAddr).AddrPort()

	f := New(types.PortAndProto{Proto: types.ProtoUDP, Port: 0}, tunnel.ClientToAgent, targetAddr, WithListenAddr(loopback))
	initCh := make(chan netip.AddrPort, 1)
	go func() {
		_ = f.Serve(ctx, initCh)
	}()
	select {
	case ap := <-initCh:
		require.True(t, ap.Addr().IsLoopback(), "packet listener must be bound to the loopback address, got %s", ap)
	case <-ctx.Done():
		t.Fatal("timeout waiting for the forwarder to start")
	}
}

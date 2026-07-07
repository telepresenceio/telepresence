package forwarder

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// recordingDialer wraps net.Dialer and records the addresses it dials, so tests
// can assert that a forwarder used the injected Dialer rather than dialing in
// its own network namespace.
type recordingDialer struct {
	net.Dialer
	mu     sync.Mutex
	dialed []string
}

func (d *recordingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, address)
	d.mu.Unlock()
	return d.Dialer.DialContext(ctx, network, address)
}

func (d *recordingDialer) dialedAddresses() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dialed...)
}

// udpEcho starts a UDP listener that echoes back whatever it receives, acting
// as the "target" of a forwarder in these tests.
func udpEcho(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteToUDP(buf[:n], addr)
		}
	}()
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestForwardUDP_WithDialer(t *testing.T) {
	ctx, cancel := context.WithTimeout(testutil.NewContext(t, false), 5*time.Second)
	defer cancel()

	target := udpEcho(t)
	targetAddr := target.LocalAddr().(*net.UDPAddr).AddrPort()

	dialer := &recordingDialer{}

	fwdConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	fwdAddr := fwdConn.LocalAddr().(*net.UDPAddr).AddrPort()

	fwCtx, fwCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- forwardUDP(fwCtx, tunnel.Tag("TST"), fwdConn, targetAddr, dialer)
	}()

	client, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(fwdAddr))
	require.NoError(t, err)
	defer client.Close()

	msg := []byte("hello")
	_, err = client.Write(msg)
	require.NoError(t, err)

	require.NoError(t, client.SetReadDeadline(time.Now().Add(3*time.Second)))
	buf := make([]byte, 1500)
	n, err := client.Read(buf)
	require.NoError(t, err)
	require.Equal(t, msg, buf[:n])

	require.Contains(t, dialer.dialedAddresses(), targetAddr.String())

	fwCancel()
	require.NoError(t, <-done)
}

func TestForwardUDP_WithoutDialer(t *testing.T) {
	ctx, cancel := context.WithTimeout(testutil.NewContext(t, false), 5*time.Second)
	defer cancel()

	target := udpEcho(t)
	targetAddr := target.LocalAddr().(*net.UDPAddr).AddrPort()

	fwdConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	fwdAddr := fwdConn.LocalAddr().(*net.UDPAddr).AddrPort()

	fwCtx, fwCancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- forwardUDP(fwCtx, tunnel.Tag("TST"), fwdConn, targetAddr, nil)
	}()

	client, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(fwdAddr))
	require.NoError(t, err)
	defer client.Close()

	msg := []byte("hello")
	_, err = client.Write(msg)
	require.NoError(t, err)

	require.NoError(t, client.SetReadDeadline(time.Now().Add(3*time.Second)))
	buf := make([]byte, 1500)
	n, err := client.Read(buf)
	require.NoError(t, err)
	require.Equal(t, msg, buf[:n])

	fwCancel()
	require.NoError(t, <-done)
}

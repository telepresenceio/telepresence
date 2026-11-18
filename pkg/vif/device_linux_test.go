package vif

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip/link/fdbased"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

// newSocketpairDevice returns a device whose fdbased endpoint is attached to a NIC of a new stack.
// The device owns one end of a socketpair and the other end is closed on cleanup.
func newSocketpairDevice(t *testing.T) (*device, *stack.Stack) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(fds[1]) })

	ep, err := fdbased.New(&fdbased.Options{
		FDs:                []int{fds[0]},
		MTU:                1500,
		PacketDispatchMode: fdbased.RecvMMsg,
	})
	if err != nil {
		_ = unix.Close(fds[0])
		t.Fatal(err)
	}
	d := &device{fd: fds[0], endPoint: ep}

	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{udp.NewProtocol},
	})
	if tcpipErr := s.CreateNIC(1, ep); tcpipErr != nil {
		d.Close()
		t.Fatalf("CreateNIC: %v", tcpipErr)
	}
	// Let the dispatcher goroutine reach its blocking poll.
	time.Sleep(100 * time.Millisecond)
	return d, s
}

func waitReturns(t *testing.T, s *stack.Stack) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stack.Wait did not return")
	}
}

// The device is closed before Wait has removed the NIC.
func TestDeviceCloseBeforeStackWait(t *testing.T) {
	d, s := newSocketpairDevice(t)
	s.Close()
	d.Close()
	waitReturns(t, s)
}

// The stack has removed the NIC before the device is closed.
func TestDeviceCloseAfterStackWait(t *testing.T) {
	d, s := newSocketpairDevice(t)
	s.Close()
	waitReturns(t, s)
	d.Close()
}

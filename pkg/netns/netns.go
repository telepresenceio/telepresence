//go:build linux

package netns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"

	"github.com/vishvananda/netns"
)

// PathForPID returns the procfs path of pid's network namespace.
func PathForPID(pid int) string {
	return fmt.Sprintf("/proc/%d/ns/net", pid)
}

// NS is an open handle to a network namespace. The handle's fd is opened
// once, by Open, and reused across any number of Do/Listen/ListenPacket/Dial
// calls, so that repeated use of the same target namespace does not pay for
// re-resolving nsPath and re-opening its fd on every call.
type NS struct {
	nsPath string
	target netns.NsHandle
}

// Open opens the network namespace at nsPath and returns a handle to it. The
// caller must Close the handle once it is no longer needed.
func Open(nsPath string) (*NS, error) {
	target, err := netns.GetFromPath(nsPath)
	if err != nil {
		return nil, fmt.Errorf("open network namespace %q: %w", nsPath, err)
	}
	return &NS{nsPath: nsPath, target: target}, nil
}

// Close closes the handle's target namespace fd.
func (n *NS) Close() error {
	return n.target.Close()
}

// Fd returns the target namespace's open file descriptor. It remains valid
// for as long as n has not been closed.
func (n *NS) Fd() int {
	return int(n.target)
}

// Do runs fn on an OS thread that has been moved into n's network namespace,
// restoring the thread to its original namespace afterwards. The thread is
// locked for the whole call so fn (and anything it starts synchronously on
// the same goroutine) observes the target namespace.
//
// The original namespace is a thread-specific property, so it is captured
// with netns.Get() on the locked OS thread on every call; only the target
// namespace's fd, held by n, is reused across calls.
//
// If restoring the original namespace fails, the thread is left LOCKED and
// not unlocked: a thread stuck in the wrong namespace must never be
// returned to the Go runtime's thread pool, so it is retired when the
// goroutine exits. This is the standard safety behaviour (see
// containernetworking/plugins' ns.Do).
func (n *NS) Do(fn func() error) error {
	runtime.LockOSThread()

	orig, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("get current network namespace: %w", err)
	}
	defer orig.Close()

	if err := netns.Set(n.target); err != nil {
		// Set failed, so (per setns semantics) the thread is still in its
		// original namespace: safe to unlock.
		runtime.UnlockOSThread()
		return fmt.Errorf("enter network namespace %q: %w", n.nsPath, err)
	}

	fnErr := fn()

	if err := netns.Set(orig); err != nil {
		// The thread could not be moved back to its original namespace, so
		// it is now stuck in the target namespace. A thread in this state
		// must never be returned to the Go runtime's thread pool, where it
		// could be reused by an unrelated goroutine that assumes it's
		// running in the normal namespace. So, unlike every other return
		// path above, we deliberately do NOT call runtime.UnlockOSThread
		// here: keeping the thread locked to this goroutine means the
		// poisoned thread is destroyed (not pooled) once the goroutine
		// exits. The deferred Close call on orig still runs as the
		// goroutine unwinds; only the unlock is skipped.
		return errors.Join(fnErr, fmt.Errorf("restore original network namespace: %w", err))
	}

	runtime.UnlockOSThread()
	return fnErr
}

// Listen creates a TCP listener bound to address inside n's network
// namespace and returns it. The returned listener remains usable from the
// caller's own namespace: only socket creation and bind happen in the
// target namespace; Accept operates on the bound fd regardless of the
// caller's current namespace.
func (n *NS) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	var (
		l    net.Listener
		lErr error
	)
	if err := n.Do(func() error {
		l, lErr = new(net.ListenConfig).Listen(ctx, network, address)
		return lErr
	}); err != nil {
		return nil, err
	}
	return l, lErr
}

// Dial establishes a connection to address from inside n's network
// namespace and returns it. Only socket creation and connect happen in the
// target namespace; the returned connection stays usable from the caller's
// own namespace, because a socket's namespace is fixed when the socket is
// created.
func (n *NS) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	var (
		conn net.Conn
		dErr error
	)
	if err := n.Do(func() error {
		conn, dErr = new(net.Dialer).DialContext(ctx, network, address)
		return dErr
	}); err != nil {
		return nil, err
	}
	return conn, dErr
}

// ListenPacket creates a packet connection bound to address inside n's
// network namespace and returns it, with the same namespace semantics as
// Listen.
func (n *NS) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	var (
		pc    net.PacketConn
		pcErr error
	)
	if err := n.Do(func() error {
		pc, pcErr = new(net.ListenConfig).ListenPacket(ctx, network, address)
		return pcErr
	}); err != nil {
		return nil, err
	}
	return pc, pcErr
}

// Do opens the network namespace at nsPath, runs fn as described by
// (*NS).Do, and closes the namespace handle again. Callers that make
// repeated calls against the same nsPath should use Open once instead, to
// avoid re-opening the namespace fd on every call.
func Do(nsPath string, fn func() error) error {
	n, err := Open(nsPath)
	if err != nil {
		return err
	}
	defer n.Close()
	return n.Do(fn)
}

// Listen is the single-call form of (*NS).Listen; see Do for why repeated
// callers should prefer Open.
func Listen(ctx context.Context, nsPath, network, address string) (net.Listener, error) {
	n, err := Open(nsPath)
	if err != nil {
		return nil, err
	}
	defer n.Close()
	return n.Listen(ctx, network, address)
}

// Dial is the single-call form of (*NS).Dial; see Do for why repeated
// callers should prefer Open.
func Dial(ctx context.Context, nsPath, network, address string) (net.Conn, error) {
	n, err := Open(nsPath)
	if err != nil {
		return nil, err
	}
	defer n.Close()
	return n.Dial(ctx, network, address)
}

// ListenPacket is the single-call form of (*NS).ListenPacket; see Do for why
// repeated callers should prefer Open.
func ListenPacket(ctx context.Context, nsPath, network, address string) (net.PacketConn, error) {
	n, err := Open(nsPath)
	if err != nil {
		return nil, err
	}
	defer n.Close()
	return n.ListenPacket(ctx, network, address)
}

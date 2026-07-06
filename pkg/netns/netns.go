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

// Do runs fn on an OS thread that has been moved into the network namespace at
// nsPath, restoring the thread to its original namespace afterwards. The thread
// is locked for the whole call so fn (and anything it starts synchronously on
// the same goroutine) observes the target namespace.
//
// If restoring the original namespace fails, the thread is left LOCKED and not
// unlocked: a thread stuck in the wrong namespace must never be returned to the
// Go runtime's thread pool, so it is retired when the goroutine exits. This is
// the standard safety behaviour (see containernetworking/plugins' ns.Do).
func Do(nsPath string, fn func() error) error {
	runtime.LockOSThread()

	orig, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("get current network namespace: %w", err)
	}
	defer orig.Close()

	target, err := netns.GetFromPath(nsPath)
	if err != nil {
		// The thread never left its original namespace, so it's safe to
		// unlock and hand back to the runtime's pool.
		runtime.UnlockOSThread()
		return fmt.Errorf("open network namespace %q: %w", nsPath, err)
	}
	defer target.Close()

	if err := netns.Set(target); err != nil {
		// Set failed, so (per setns semantics) the thread is still in its
		// original namespace: safe to unlock.
		runtime.UnlockOSThread()
		return fmt.Errorf("enter network namespace %q: %w", nsPath, err)
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
		// exits. The deferred Close calls on orig and target still run as
		// the goroutine unwinds; only the unlock is skipped.
		return errors.Join(fnErr, fmt.Errorf("restore original network namespace: %w", err))
	}

	runtime.UnlockOSThread()
	return fnErr
}

// Listen creates a TCP listener bound to address inside the network namespace
// at nsPath and returns it. The returned listener remains usable from the
// caller's own namespace: only socket creation and bind happen in the target
// namespace; Accept operates on the bound fd regardless of the caller's current
// namespace.
func Listen(ctx context.Context, nsPath, network, address string) (net.Listener, error) {
	var (
		l    net.Listener
		lErr error
	)
	if err := Do(nsPath, func() error {
		l, lErr = new(net.ListenConfig).Listen(ctx, network, address)
		return lErr
	}); err != nil {
		return nil, err
	}
	return l, lErr
}

// ListenPacket creates a packet connection bound to address inside the network
// namespace at nsPath and returns it, with the same namespace semantics as
// Listen.
func ListenPacket(ctx context.Context, nsPath, network, address string) (net.PacketConn, error) {
	var (
		pc    net.PacketConn
		pcErr error
	)
	if err := Do(nsPath, func() error {
		pc, pcErr = new(net.ListenConfig).ListenPacket(ctx, network, address)
		return pcErr
	}); err != nil {
		return nil, err
	}
	return pc, pcErr
}

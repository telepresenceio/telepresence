//go:build linux

package netns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	vnetns "github.com/vishvananda/netns"
)

func TestPathForPID(t *testing.T) {
	assert.Equal(t, "/proc/1234/ns/net", PathForPID(1234))
	assert.Equal(t, fmt.Sprintf("/proc/%d/ns/net", os.Getpid()), PathForPID(os.Getpid()))
}

// TestDo_OwnNamespace exercises Do against the caller's OWN network
// namespace. Entering the namespace you are already in requires no extra
// privilege, so this is safe to run in an unprivileged CI sandbox.
func TestDo_OwnNamespace(t *testing.T) {
	// Best-effort baseline handle. netns.Get needs no special privilege
	// (it just opens the current thread's own /proc/self/task/<tid>/ns/net),
	// but be defensive in case some sandbox restricts even that.
	before, err := vnetns.Get()
	if err != nil {
		t.Skipf("netns.Get failed in this environment, skipping: %v", err)
	}
	defer before.Close()

	selfPath := PathForPID(os.Getpid())

	ran := false
	sentinel := errors.New("sentinel error from fn")
	doErr := Do(selfPath, func() error {
		ran = true
		return sentinel
	})
	if !ran && errors.Is(doErr, unix.EPERM) {
		// setns(2) requires CAP_SYS_ADMIN even when entering the caller's
		// own namespace; some sandboxes (e.g. this one) don't grant it.
		t.Skipf("entering own network namespace forbidden in this environment, skipping: %v", doErr)
	}
	assert.True(t, ran, "fn should have run")
	assert.ErrorIs(t, doErr, sentinel, "Do should propagate fn's error")

	// Best-effort leak check: a freshly locked thread should still observe
	// the same namespace the test started in, i.e. Do restored it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	after, err := vnetns.Get()
	if err != nil {
		t.Skipf("netns.Get failed in this environment, skipping leak check: %v", err)
	}
	defer after.Close()
	assert.True(t, before.Equal(after), "current namespace should be unchanged after Do returns")
}

// TestListen_OwnNamespace exercises Listen against the caller's own network
// namespace path, so it needs no extra privilege beyond what's required to
// bind a port at all.
func TestListen_OwnNamespace(t *testing.T) {
	selfPath := PathForPID(os.Getpid())

	l, err := Listen(context.Background(), selfPath, "tcp", ":0")
	if err != nil {
		if errors.Is(err, unix.EPERM) {
			t.Skipf("listen forbidden in this environment, skipping: %v", err)
		}
		require.NoError(t, err)
	}
	require.NotNil(t, l)
	defer l.Close()

	addr, ok := l.Addr().(*net.TCPAddr)
	require.True(t, ok, "expected a *net.TCPAddr, got %T", l.Addr())
	assert.NotZero(t, addr.Port)

	conn, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

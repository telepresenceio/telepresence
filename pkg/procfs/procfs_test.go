//go:build linux

package procfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEnviron_ChildProcess exercises Environ against a real /proc/<pid>/environ.
//
// A process's /proc/<pid>/environ is a snapshot of the environment block
// installed by execve; it is not kept in sync with setenv/os.Setenv calls
// made afterwards in that process's own lifetime (glibc's setenv reallocates
// storage on the heap, never touching the kernel's copy of the original
// exec-time block). So the marker has to come from a freshly exec'd child,
// which is also what Environ is used for in practice: reading another
// process's environment as of when it started.
func TestEnviron_ChildProcess(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.Env = append(os.Environ(), "PROCFS_TEST_MARKER=x")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// cmd.Start returns once fork(2) succeeds, which can race the
	// child's own execve(2) that installs cmd.Env as its environ; poll
	// until the exec has completed.
	require.Eventually(t, func() bool {
		env, err := Environ(cmd.Process.Pid)
		return err == nil && slices.Contains(env, "PROCFS_TEST_MARKER=x")
	}, time.Second, 5*time.Millisecond)
}

// TestEnviron_OrderAndNoEquals verifies that Environ preserves the raw
// entries of /proc/<pid>/environ in their original order, and passes
// through an entry that has no '=' rather than dropping it. (os/exec itself
// collapses duplicate keys in Cmd.Env down to the last value before
// exec'ing, so the kernel's copy of the child's environment block never
// actually contains duplicates to exercise here -- but nothing about
// Environ's parsing depends on keys being unique.)
func TestEnviron_OrderAndNoEquals(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.Env = append(os.Environ(),
		"PROCFS_TEST_FIRST=1",
		"PROCFS_TEST_NOEQUALS",
		"PROCFS_TEST_MARKER=last")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	var env []string
	require.Eventually(t, func() bool {
		var err error
		env, err = Environ(cmd.Process.Pid)
		return err == nil && slices.Contains(env, "PROCFS_TEST_MARKER=last")
	}, time.Second, 5*time.Millisecond)

	first := slices.Index(env, "PROCFS_TEST_FIRST=1")
	noEquals := slices.Index(env, "PROCFS_TEST_NOEQUALS")
	marker := slices.Index(env, "PROCFS_TEST_MARKER=last")

	require.NotEqual(t, -1, first)
	require.NotEqual(t, -1, noEquals, "entry without '=' not found")
	require.NotEqual(t, -1, marker)
	require.Less(t, first, noEquals, "entries must retain their original order")
	require.Less(t, noEquals, marker)
}

func TestEnviron_NoSuchPid(t *testing.T) {
	_, err := Environ(noSuchPid)
	require.Error(t, err)
}

func TestRootPath(t *testing.T) {
	require.Equal(t, "/proc/1234/root", RootPath(1234))
	require.Equal(t, "/proc/1234/root/etc/hostname", RootPath(1234, "etc", "hostname"))
}

func TestRootPath_ResolvesSelf(t *testing.T) {
	pid := os.Getpid()

	// RootPath(pid) is itself a symlink (it resolves through the magic
	// /proc/<pid>/root link), so this must follow it with Stat rather
	// than check the link with Lstat.
	info, err := os.Stat(RootPath(pid))
	require.NoError(t, err)
	require.True(t, info.IsDir())

	tmpFile := filepath.Join(t.TempDir(), "marker")
	require.NoError(t, os.WriteFile(tmpFile, []byte("hello"), 0o600))

	// /proc/self/root is a symlink to "/" in the caller's own mount
	// namespace, so the absolute tmpFile path resolves through it.
	data, err := os.ReadFile(RootPath(pid, tmpFile))
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
}

func TestUIDMap_Self(t *testing.T) {
	entries, err := UIDMap(os.Getpid())
	require.NoError(t, err)
	require.NotEmpty(t, entries)
}

func TestInUserNamespace_Self(t *testing.T) {
	entries, err := UIDMap(os.Getpid())
	require.NoError(t, err)

	inUserNS, err := InUserNamespace(os.Getpid())
	require.NoError(t, err)

	isInit := len(entries) == 1 &&
		entries[0].InsideID == 0 && entries[0].OutsideID == 0 && entries[0].Length == 4294967295
	require.Equal(t, !isInit, inUserNS)
}

// noSuchPid is a pid that is certainly not in use: above even the largest
// configurable pid_max (2^22 on 64-bit kernels).
const noSuchPid = 1 << 30

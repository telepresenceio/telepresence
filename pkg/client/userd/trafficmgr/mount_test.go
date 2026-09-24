package trafficmgr

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestResolveMountPoint(t *testing.T) {
	root := t.TempDir()
	cfg := client.GetDefaultConfig()
	cfg.Intercept().MountsRoot = root
	ctx := client.WithConfig(context.Background(), cfg)

	t.Run("true sentinel creates a directory the daemon owns", func(t *testing.T) {
		dir, owns, err := resolveMountPoint(ctx, "true")
		require.NoError(t, err)
		require.True(t, owns)
		require.DirExists(t, dir)
		require.Equal(t, root, filepath.Dir(dir))
	})

	t.Run("explicit path passes through unowned", func(t *testing.T) {
		dir, owns, err := resolveMountPoint(ctx, "/some/explicit/path")
		require.NoError(t, err)
		require.False(t, owns)
		require.Equal(t, "/some/explicit/path", dir)
	})

	t.Run("empty string passes through unowned", func(t *testing.T) {
		dir, owns, err := resolveMountPoint(ctx, "")
		require.NoError(t, err)
		require.False(t, owns)
		require.Equal(t, "", dir)
	})
}

func TestRemoveOwnedMountPoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mount points are drive letters on Windows; nothing is removed")
	}
	ctx := context.Background()

	t.Run("removes an owned directory and clears ownership", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "telfs-owned")
		require.NoError(t, os.Mkdir(dir, 0o700))

		var owns atomic.Bool
		owns.Store(true)

		removeOwnedMountPoint(ctx, &owns, dir)
		require.NoDirExists(t, dir)
		require.False(t, owns.Load())
	})

	t.Run("a second call is a no-op", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "telfs-owned")
		require.NoError(t, os.Mkdir(dir, 0o700))

		var owns atomic.Bool
		owns.Store(true)

		removeOwnedMountPoint(ctx, &owns, dir)
		require.NoDirExists(t, dir)

		// Simulates a manager-initiated removal racing an explicit leave: must not panic
		// or attempt a second removal now that ownership has been cleared.
		removeOwnedMountPoint(ctx, &owns, dir)
	})

	t.Run("leaves an unowned directory alone", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "telfs-explicit")
		require.NoError(t, os.Mkdir(dir, 0o700))

		var owns atomic.Bool // never set: e.g. an explicit --mount path
		removeOwnedMountPoint(ctx, &owns, dir)
		require.DirExists(t, dir)
	})
}

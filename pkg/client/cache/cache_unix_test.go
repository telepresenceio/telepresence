//go:build !windows

package cache_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func TestSaveToUserCache_DoesNotFollowLinks(t *testing.T) {
	dir := t.TempDir()
	ctx := filelocation.WithAppUserCacheDir(t.Context(), dir)
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "a.json")))
	missing := filepath.Join(t.TempDir(), "missing")
	require.NoError(t, os.Symlink(missing, filepath.Join(dir, "b.json")))

	for _, perm := range []cache.Permissions{cache.Public, cache.Private} {
		require.Error(t, cache.SaveToUserCache(ctx, map[string]string{"a": "b"}, "a.json", perm))
		require.Error(t, cache.SaveToUserCache(ctx, map[string]string{"a": "b"}, "b.json", perm))
	}
	b, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(b))
	assert.NoFileExists(t, missing)
}

func TestSaveToUserCache_WritesRegularFiles(t *testing.T) {
	dir := t.TempDir()
	ctx := filelocation.WithAppUserCacheDir(t.Context(), dir)
	require.NoError(t, cache.SaveToUserCache(ctx, map[string]string{"a": "b"}, "x/a.json", cache.Private))
	fi, err := os.Stat(filepath.Join(dir, "x", "a.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
}

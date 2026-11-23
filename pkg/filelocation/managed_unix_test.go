//go:build !windows

package filelocation

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func managedTree(t *testing.T) (root, file string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "managed")
	dir := filepath.Join(root, "rootd")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(root, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))
	file = filepath.Join(dir, "daemon.json")
	require.NoError(t, os.WriteFile(file, []byte("{}"), 0o644))
	require.NoError(t, os.Chmod(file, 0o644))
	return root, file
}

func TestCheckManaged(t *testing.T) {
	owner := uint32(os.Getuid())

	t.Run("good tree", func(t *testing.T) {
		root, file := managedTree(t)
		assert.NoError(t, checkManagedOwner(file, root, owner))
	})
	t.Run("wrong owner", func(t *testing.T) {
		root, file := managedTree(t)
		var ue *UntrustedError
		require.ErrorAs(t, checkManagedOwner(file, root, owner+1), &ue)
		assert.Contains(t, ue.Reason, "owned by")
	})
	t.Run("group writable directory", func(t *testing.T) {
		root, file := managedTree(t)
		require.NoError(t, os.Chmod(filepath.Dir(file), 0o775))
		var ue *UntrustedError
		require.ErrorAs(t, checkManagedOwner(file, root, owner), &ue)
		assert.Contains(t, ue.Reason, "writable")
	})
	t.Run("group writable root", func(t *testing.T) {
		root, file := managedTree(t)
		require.NoError(t, os.Chmod(root, 0o775))
		var ue *UntrustedError
		require.ErrorAs(t, checkManagedOwner(file, root, owner), &ue)
	})
	t.Run("world writable file", func(t *testing.T) {
		root, file := managedTree(t)
		require.NoError(t, os.Chmod(file, 0o646))
		var ue *UntrustedError
		require.ErrorAs(t, checkManagedOwner(file, root, owner), &ue)
		assert.Contains(t, ue.Reason, "writable")
	})
	t.Run("symlinked directory", func(t *testing.T) {
		root, file := managedTree(t)
		realDir := filepath.Join(root, "real")
		require.NoError(t, os.Rename(filepath.Dir(file), realDir))
		require.NoError(t, os.Symlink(realDir, filepath.Dir(file)))
		var ue *UntrustedError
		require.ErrorAs(t, checkManagedOwner(file, root, owner), &ue)
		assert.Contains(t, ue.Reason, "symbolic link")
	})
	t.Run("symlinked file", func(t *testing.T) {
		root, file := managedTree(t)
		other := filepath.Join(root, "other.json")
		require.NoError(t, os.Rename(file, other))
		require.NoError(t, os.Symlink(other, file))
		var ue *UntrustedError
		require.ErrorAs(t, checkManagedOwner(file, root, owner), &ue)
	})
	t.Run("outside the root", func(t *testing.T) {
		root, _ := managedTree(t)
		var ue *UntrustedError
		require.ErrorAs(t, checkManagedOwner(filepath.Join(filepath.Dir(root), "x"), root, owner), &ue)
	})
	t.Run("missing file", func(t *testing.T) {
		root, file := managedTree(t)
		require.NoError(t, os.Remove(file))
		err := checkManagedOwner(file, root, owner)
		require.Error(t, err)
		assert.True(t, errors.Is(err, fs.ErrNotExist))
		var ue *UntrustedError
		assert.False(t, errors.As(err, &ue))
	})
}

func TestCheckManaged_NotRootOwnedHere(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the test files would be owned by root")
	}
	root, file := managedTree(t)
	var ue *UntrustedError
	require.ErrorAs(t, CheckManaged(file, root), &ue)
}

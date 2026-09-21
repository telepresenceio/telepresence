package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_resolveInRoot_plainPath(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "a", "b"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a", "b", "c"), []byte("x"), 0o600))

	got, err := resolveInRoot(root, "a/b/c")
	require.NoError(t, err)
	require.Equal(t, "a/b/c", got)
}

func Test_resolveInRoot_relativeSymlink(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "run", "secrets", "kubernetes.io"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var"), 0o700))
	require.NoError(t, os.Symlink("../run", filepath.Join(root, "var", "run")))

	got, err := resolveInRoot(root, "var/run/secrets/kubernetes.io")
	require.NoError(t, err)
	require.Equal(t, "run/secrets/kubernetes.io", got)
}

func Test_resolveInRoot_absoluteSymlink(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "run", "secrets", "kubernetes.io"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "var"), 0o700))
	require.NoError(t, os.Symlink("/run", filepath.Join(root, "var", "run")))

	got, err := resolveInRoot(root, "var/run/secrets/kubernetes.io")
	require.NoError(t, err)
	require.Equal(t, "run/secrets/kubernetes.io", got)
}

func Test_resolveInRoot_chainOfLinks(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "final"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "final", "x"), []byte("x"), 0o600))
	require.NoError(t, os.Symlink("mid", filepath.Join(root, "start")))
	require.NoError(t, os.Symlink("final", filepath.Join(root, "mid")))

	got, err := resolveInRoot(root, "start/x")
	require.NoError(t, err)
	require.Equal(t, "final/x", got)
}

func Test_resolveInRoot_dotDotClampsAtRoot(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "escaped"), 0o700))

	got, err := resolveInRoot(root, "../../escaped")
	require.NoError(t, err)
	require.Equal(t, "escaped", got)
}

func Test_resolveInRoot_missingComponentErrors(t *testing.T) {
	root := t.TempDir()

	_, err := resolveInRoot(root, "nope/foo")
	require.Error(t, err)
}

func Test_resolveInRoot_symlinkLoopErrors(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Symlink("b", filepath.Join(root, "a")))
	require.NoError(t, os.Symlink("a", filepath.Join(root, "b")))

	_, err := resolveInRoot(root, "a")
	require.Error(t, err)
}

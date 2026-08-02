package sftpserver_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/agent/sftpserver"
)

// testTree is the fixture a newTestServer builds: an exports root containing an "app"
// directory with a regular file, a subdirectory holding an internal symlink, and two
// symlinks -- one absolute, one relative -- that both point outside the exports root. This
// models a plain layout with no mounts-tree indirection, e.g. content addAppMounts would
// never touch, and the layout the sftpserver unit tests used before dual-root resolution
// existed.
type testTree struct {
	root    string
	appDir  string
	outside string
}

// startServer starts an sftpserver.Server over a net.Pipe and returns a connected client,
// closing both down on test cleanup.
func startServer(t *testing.T, srv *sftpserver.Server) *sftp.Client {
	t.Helper()

	clientConn, serverConn := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())

	serveDone := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx, serverConn)
		close(serveDone)
	}()

	client, err := sftp.NewClientPipe(clientConn, clientConn)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = client.Close()
		cancel()
		<-serveDone
	})

	return client
}

// newTestServer builds a testTree under t.TempDir(), starts a sftpserver.Server confined to
// it as the exports root (with an unrelated, untouched mounts root), and returns a connected
// sftp.Client together with the tree.
func newTestServer(t *testing.T) (*sftp.Client, testTree) {
	t.Helper()

	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "sub", "real"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "data.txt"), []byte("hello"), 0o644))

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("outside"), 0o644))

	require.NoError(t, os.Symlink(outside, filepath.Join(appDir, "abs-escape")))
	require.NoError(t, os.Symlink("../../../../../../../etc", filepath.Join(appDir, "rel-escape")))
	// "int" sits one level below "app" (inside "sub") rather than directly in it: a symlink
	// found directly under a container directory is, per addAppMounts, either absent or an
	// absolute link into the mounts tree, so resolve treats any other symlink found there as
	// hostile. One level deeper, it is plain content and os.Root follows it natively.
	require.NoError(t, os.Symlink("real", filepath.Join(appDir, "sub", "int")))

	srv, err := sftpserver.New(root, t.TempDir())
	require.NoError(t, err)

	client := startServer(t, srv)

	return client, testTree{root: root, appDir: appDir, outside: outside}
}

func readAll(t *testing.T, client *sftp.Client, path string) string {
	t.Helper()
	f, err := client.Open(path)
	require.NoError(t, err)
	defer f.Close()
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	return string(data)
}

func TestReadPaths(t *testing.T) {
	client, _ := newTestServer(t)

	for _, p := range []string{
		"/tel_app_exports/app/data.txt",
		"app/data.txt",
		"/app/data.txt",
	} {
		require.Equal(t, "hello", readAll(t, client, p), "path %q", p)
	}
}

func TestReadDirRoot(t *testing.T) {
	client, _ := newTestServer(t)

	for _, p := range []string{"/", "/tel_app_exports"} {
		entries, err := client.ReadDir(p)
		require.NoError(t, err, "path %q", p)
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		require.Contains(t, names, "app", "path %q", p)
	}
}

func TestEscapesAreBlocked(t *testing.T) {
	client, _ := newTestServer(t)

	_, err := client.Open("/etc/passwd")
	require.Error(t, err)

	_, err = client.Open("/tel_app_exports/app/abs-escape/outside.txt")
	require.Error(t, err)

	_, err = client.Stat("/tel_app_exports/app/abs-escape/outside.txt")
	require.Error(t, err)

	_, err = client.Open("/tel_app_exports/app/rel-escape/passwd")
	require.Error(t, err)

	_, err = client.Stat("/tel_app_exports/app/rel-escape/passwd")
	require.Error(t, err)
}

func TestInternalSymlinkWorks(t *testing.T) {
	client, _ := newTestServer(t)

	entries, err := client.ReadDir("/tel_app_exports/app/sub/int")
	require.NoError(t, err)
	require.Empty(t, entries)

	fi, err := client.Stat("/tel_app_exports/app/sub/int")
	require.NoError(t, err)
	require.True(t, fi.IsDir())
}

func TestWritePath(t *testing.T) {
	client, _ := newTestServer(t)

	const newPath = "/tel_app_exports/app/new.txt"
	const otherPath = "/tel_app_exports/app/other.txt"
	const dirPath = "/tel_app_exports/app/newdir"

	w, err := client.Create(newPath)
	require.NoError(t, err)
	_, err = w.Write([]byte("created"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.Equal(t, "created", readAll(t, client, newPath))

	require.NoError(t, client.Mkdir(dirPath))
	fi, err := client.Stat(dirPath)
	require.NoError(t, err)
	require.True(t, fi.IsDir())

	o, err := client.Create(otherPath)
	require.NoError(t, err)
	_, err = o.Write([]byte("other"))
	require.NoError(t, err)
	require.NoError(t, o.Close())

	// Rename fails when the target already exists.
	err = client.Rename(newPath, otherPath)
	require.Error(t, err)

	// PosixRename overwrites it.
	require.NoError(t, client.PosixRename(newPath, otherPath))
	require.Equal(t, "created", readAll(t, client, otherPath))
	_, err = client.Stat(newPath)
	require.Error(t, err)

	require.NoError(t, client.Chmod(otherPath, 0o600))
	fi, err = client.Stat(otherPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	require.NoError(t, client.Remove(otherPath))
	_, err = client.Stat(otherPath)
	require.Error(t, err)
}

func TestStatOutsideTreeFails(t *testing.T) {
	client, tree := newTestServer(t)

	_, err := client.Stat("/" + filepath.Base(tree.outside) + "/outside.txt")
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// mountsTree is the fixture a newMountsTestServer builds: an exports root and a separate
// mounts root wired together the way cmd/traffic/cmd/agent/config.go's addAppMounts wires
// agentconfig.ExportsMountPoint to agentconfig.MountPrefixApp on a real pod -- one absolute
// symlink per exported top-level mount, kubelet-style "..data" indirection underneath, an
// unexported top with no exports-side symlink, and two hostile symlinks that must stay
// blocked.
type mountsTree struct {
	exportsRoot string
	mountsRoot  string
	container   string
}

// newMountsTestServer builds a mountsTree under two t.TempDir() trees, starts a
// sftpserver.Server over them, and returns a connected sftp.Client together with the tree.
func newMountsTestServer(t *testing.T) (*sftp.Client, mountsTree) {
	t.Helper()

	const c = "mounts-content"
	mountsRoot := t.TempDir()
	exportsRoot := t.TempDir()

	// A kubelet-style ConfigMap mount: a versioned data directory, a "..data" symlink to
	// it, and a file symlink reached through "..data".
	cfgDir := filepath.Join(mountsRoot, c, "etc", "rtest-config")
	dataDir := filepath.Join(cfgDir, "..2026_data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "rtest.conf"), []byte("rtest=1"), 0o644))
	require.NoError(t, os.Symlink("..2026_data", filepath.Join(cfgDir, "..data")))
	require.NoError(t, os.Symlink("..data/rtest.conf", filepath.Join(cfgDir, "rtest.conf")))

	// An unexported top: present under the mounts root (as it would be under the app
	// container's real mount point), but its policy is ignore/local, so addAppMounts never
	// symlinked it from the exports side; it must stay invisible there.
	require.NoError(t, os.MkdirAll(filepath.Join(mountsRoot, c, "hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mountsRoot, c, "hidden", "secret.txt"), []byte("secret"), 0o644))

	// A symlink inside the volume content itself, pointing at an absolute path outside the
	// mounts tree; os.Root refuses any absolute symlink, so this must fail like any other
	// escape attempt.
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(mountsRoot, c, "etc", "escape")))

	// The exports tree: one absolute symlink per exported top, exactly as addAppMounts
	// writes it, plus a hostile one whose target lies outside the mounts root entirely.
	require.NoError(t, os.MkdirAll(filepath.Join(exportsRoot, c), 0o755))
	require.NoError(t, os.Symlink(filepath.Join(mountsRoot, c, "etc"), filepath.Join(exportsRoot, c, "etc")))
	require.NoError(t, os.Symlink("/etc", filepath.Join(exportsRoot, c, "evil")))

	srv, err := sftpserver.New(exportsRoot, mountsRoot)
	require.NoError(t, err)

	client := startServer(t, srv)

	return client, mountsTree{exportsRoot: exportsRoot, mountsRoot: mountsRoot, container: c}
}

func TestMountsLinkReadWorks(t *testing.T) {
	client, tree := newMountsTestServer(t)
	base := "/tel_app_exports/" + tree.container + "/etc/rtest-config"

	// Both the direct file symlink and the kubelet "..data" chain resolve to the same
	// content, through the exports symlink into the mounts tree.
	require.Equal(t, "rtest=1", readAll(t, client, base+"/rtest.conf"))
	require.Equal(t, "rtest=1", readAll(t, client, base+"/..data/rtest.conf"))

	fi, err := client.Stat(base + "/rtest.conf")
	require.NoError(t, err)
	require.False(t, fi.IsDir())

	entries, err := client.ReadDir(base)
	require.NoError(t, err)
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	require.Contains(t, names, "..2026_data")
	require.Contains(t, names, "..data")
	require.Contains(t, names, "rtest.conf")

	// Lstat on the mount-top link itself still shows a symlink, not the directory it
	// points to.
	lf, err := client.Lstat("/tel_app_exports/" + tree.container + "/etc")
	require.NoError(t, err)
	require.NotEqual(t, os.FileMode(0), lf.Mode()&os.ModeSymlink)

	// Stat through the same path resolves it, showing the real directory.
	sf, err := client.Stat("/tel_app_exports/" + tree.container + "/etc")
	require.NoError(t, err)
	require.True(t, sf.IsDir())
}

func TestMountsLinkWriteWorks(t *testing.T) {
	client, tree := newMountsTestServer(t)
	base := "/tel_app_exports/" + tree.container + "/etc"

	// Create a new file through the link and confirm it lands in the real mounts tree.
	newPath := base + "/new.txt"
	w, err := client.Create(newPath)
	require.NoError(t, err)
	_, err = w.Write([]byte("created"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.Equal(t, "created", readAll(t, client, newPath))
	data, err := os.ReadFile(filepath.Join(tree.mountsRoot, tree.container, "etc", "new.txt"))
	require.NoError(t, err)
	require.Equal(t, "created", string(data))

	// Mkdir through the link.
	dirPath := base + "/newdir"
	require.NoError(t, client.Mkdir(dirPath))
	fi, err := os.Stat(filepath.Join(tree.mountsRoot, tree.container, "etc", "newdir"))
	require.NoError(t, err)
	require.True(t, fi.IsDir())

	// Rename/PosixRename through the link.
	otherPath := base + "/other.txt"
	o, err := client.Create(otherPath)
	require.NoError(t, err)
	require.NoError(t, o.Close())
	require.NoError(t, client.PosixRename(newPath, otherPath))
	require.Equal(t, "created", readAll(t, client, otherPath))
	_, err = client.Stat(newPath)
	require.Error(t, err)

	// Chmod through the link.
	require.NoError(t, client.Chmod(otherPath, 0o600))
	fi, err = client.Stat(otherPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	// Remove through the link.
	require.NoError(t, client.Remove(otherPath))
	_, err = client.Stat(otherPath)
	require.Error(t, err)
}

func TestUnexportedTopIsInvisible(t *testing.T) {
	client, tree := newMountsTestServer(t)

	_, err := client.Stat("/tel_app_exports/" + tree.container + "/hidden")
	require.Error(t, err)
	require.ErrorIs(t, err, os.ErrNotExist)

	_, err = client.Open("/tel_app_exports/" + tree.container + "/hidden/secret.txt")
	require.Error(t, err)

	entries, err := client.ReadDir("/tel_app_exports/" + tree.container)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotEqual(t, "hidden", e.Name())
	}
}

func TestHostileSymlinksAreBlocked(t *testing.T) {
	client, tree := newMountsTestServer(t)

	// The exports-tree symlink whose target lies outside the mounts root entirely.
	_, err := client.Stat("/tel_app_exports/" + tree.container + "/evil")
	require.Error(t, err)
	_, err = client.Open("/tel_app_exports/" + tree.container + "/evil/passwd")
	require.Error(t, err)

	// A symlink inside the mounted content itself, pointing outside the mounts tree.
	_, err = client.Stat("/tel_app_exports/" + tree.container + "/etc/escape")
	require.Error(t, err)
	_, err = client.Open("/tel_app_exports/" + tree.container + "/etc/escape")
	require.Error(t, err)
}

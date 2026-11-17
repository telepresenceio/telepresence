package daemon_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

func cacheCtx(t *testing.T) (context.Context, string) {
	t.Helper()
	dir := t.TempDir()
	return filelocation.WithAppUserCacheDir(t.Context(), dir), dir
}

func TestSaveInfo_UserDaemonInfoIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
	ctx, dir := cacheCtx(t)
	path := filepath.Join(dir, "userd", daemon.InfoFileName)

	il := daemon.NewUserInfoLoader(ctx)
	require.NoError(t, il.SaveInfo(&daemon.Info{DaemonPort: 1234, Token: "secret"}, daemon.InfoFileName))
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())

	loaded, err := il.LoadInfo(daemon.InfoFileName)
	require.NoError(t, err)
	assert.Equal(t, "secret", loaded.Token)
	assert.Equal(t, uint16(1234), loaded.DaemonPort)
}

func TestSaveInfo_ExistingFileBecomesPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
	ctx, dir := cacheCtx(t)
	path := filepath.Join(dir, "userd", daemon.InfoFileName)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"daemon_port":1}`), 0o644))
	require.NoError(t, os.Chmod(path, 0o644))

	require.NoError(t, daemon.NewUserInfoLoader(ctx).SaveInfo(&daemon.Info{DaemonPort: 1234, Token: "secret"}, daemon.InfoFileName))
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

func TestSaveInfo_RootDaemonInfoMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
	ctx, dir := cacheCtx(t)
	require.NoError(t, daemon.NewRootInfoLoader(ctx, false).SaveInfo(&daemon.RootInfo{DaemonPort: 1234}, daemon.InfoFileName))
	st, err := os.Stat(filepath.Join(dir, "rootd", daemon.InfoFileName))
	require.NoError(t, err)

	// The mode is the one that a plain write of a public file gets, with the umask applied.
	ref := filepath.Join(dir, "ref")
	require.NoError(t, os.WriteFile(ref, nil, 0o644))
	refSt, err := os.Stat(ref)
	require.NoError(t, err)
	assert.Equal(t, refSt.Mode().Perm(), st.Mode().Perm())
}

func TestLoadUserDaemonToken(t *testing.T) {
	ctx, _ := cacheCtx(t)
	il := daemon.NewUserInfoLoader(ctx)
	require.NoError(t, il.SaveInfo(&daemon.Info{DaemonPort: 1234, Token: "secret"}, daemon.InfoFileName))
	require.NoError(t, il.SaveInfo(&daemon.Info{DaemonPort: 1234}, "no-token.json"))

	token, err := daemon.LoadUserDaemonToken(ctx, daemon.InfoFileName)
	require.NoError(t, err)
	assert.Equal(t, "secret", token)

	_, err = daemon.LoadUserDaemonToken(ctx, "no-token.json")
	assert.Error(t, err)
	_, err = daemon.LoadUserDaemonToken(ctx, "missing.json")
	assert.Error(t, err)
}

func TestDialUserDaemon_PresentsTokenFromInfoFile(t *testing.T) {
	const token = "right-token"
	ctx, _ := cacheCtx(t)

	svc := server.NewWithAuth(ctx, server.TokenInterceptors(token))
	healthpb.RegisterHealthServer(svc, health.NewServer())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = svc.Serve(lis) }()
	t.Cleanup(svc.Stop)
	port := uint16(lis.Addr().(*net.TCPAddr).Port)

	il := daemon.NewUserInfoLoader(ctx)
	require.NoError(t, il.SaveInfo(&daemon.Info{DaemonPort: port, Token: token}, daemon.InfoFileName))

	conn, err := daemon.DialUserDaemon(ctx, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	hc := healthpb.NewHealthClient(conn)

	_, err = hc.Check(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err, "unary call")

	ws, err := hc.Watch(ctx, &healthpb.HealthCheckRequest{})
	require.NoError(t, err)
	_, err = ws.Recv()
	require.NoError(t, err, "streaming call")

	// Without the token in the info file, the daemon refuses the same calls.
	require.NoError(t, il.SaveInfo(&daemon.Info{DaemonPort: port}, daemon.InfoFileName))
	conn2, err := daemon.DialUserDaemon(ctx, false)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn2.Close() })
	_, err = healthpb.NewHealthClient(conn2).Check(ctx, &healthpb.HealthCheckRequest{})
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

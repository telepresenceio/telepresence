package daemon

import (
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func TestDaemonToken(t *testing.T) {
	ctx := filelocation.WithAppUserCacheDir(t.Context(), t.TempDir())
	require.NoError(t, daemon.NewUserInfoLoader(ctx).SaveInfo(&daemon.Info{Token: "from-file"}, daemon.InfoFileName))
	require.NoError(t, daemon.NewUserInfoLoader(ctx).SaveInfo(&daemon.Info{}, "empty.json"))

	flags := Command(ctx).Flags()

	t.Run("nothing", func(t *testing.T) {
		t.Setenv(daemon.TokenEnvName, "")
		_, err := daemonToken(ctx, flags)
		assert.Error(t, err)
	})

	t.Run("environment", func(t *testing.T) {
		t.Setenv(daemon.TokenEnvName, "from-env")
		token, err := daemonToken(ctx, flags)
		require.NoError(t, err)
		assert.Equal(t, "from-env", token)
	})

	t.Run("info file", func(t *testing.T) {
		t.Setenv(daemon.TokenEnvName, "from-env")
		fl := Command(ctx).Flags()
		require.NoError(t, fl.Set(infoFileFlag, daemon.InfoFileName))
		token, err := daemonToken(ctx, fl)
		require.NoError(t, err)
		assert.Equal(t, "from-file", token, "the info file takes precedence over the environment")
	})

	t.Run("info file without token", func(t *testing.T) {
		t.Setenv(daemon.TokenEnvName, "from-env")
		fl := Command(ctx).Flags()
		require.NoError(t, fl.Set(infoFileFlag, "empty.json"))
		_, err := daemonToken(ctx, fl)
		assert.Error(t, err)
	})

	t.Run("missing info file", func(t *testing.T) {
		fl := Command(ctx).Flags()
		require.NoError(t, fl.Set(infoFileFlag, "missing.json"))
		_, err := daemonToken(ctx, fl)
		assert.Error(t, err)
	})
}

// A daemon that has no secret must fail before it opens its listener.
func TestInternalRun_RefusesToServeWithoutSecret(t *testing.T) {
	dir := t.TempDir()
	ctx := filelocation.WithAppUserCacheDir(t.Context(), filepath.Join(dir, "cache"))
	ctx = filelocation.WithAppUserLogDir(ctx, filepath.Join(dir, "logs"))
	ctx = filelocation.WithAppUserConfigDir(ctx, filepath.Join(dir, "config"))
	t.Setenv(daemon.TokenEnvName, "")

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())

	flags := Command(ctx).Flags()
	require.NoError(t, flags.Set(addressFlag, addr))
	require.NoError(t, flags.Set(logfileFlag, filepath.Join(dir, "logs", "connector.log")))

	err = internalRun(ctx, flags)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no secret")

	// Nothing listens on the address.
	conn, err := net.Dial("tcp", addr)
	if err == nil {
		_ = conn.Close()
		t.Fatal("the daemon is listening although it has no secret")
	}
}

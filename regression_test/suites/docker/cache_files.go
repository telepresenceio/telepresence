//go:build linux

package docker

import (
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// cacheFilesConnName names the named docker connection this suite creates.
const cacheFilesConnName = "rtest-cachefiles"

// cacheFileTimeout bounds the poll for the containerized daemon's cache
// write to appear on the host.
const cacheFileTimeout = 15 * time.Second

// CacheFiles proves a containerized daemon's writes to the shared cache
// directory land on the host owned by the invoking user, not root.
//
// The framework isolates the connect config/log dirs under the run's own
// home (DEV_TELEPRESENCE_CONFIG_DIR/DEV_TELEPRESENCE_LOG_DIR -- see
// rt/runtime.go's newRuntime/childEnv), but not the cache dir: pkg/client/
// cli/main.go only recognizes those two env vars, so filelocation.
// AppUserCacheDir resolves, for both this test process and the CLI
// subprocess it drives, to the invoking user's real, unspoofed
// $HOME/.cache/telepresence -- the same host directory pkg/client/docker/
// daemon.go's DaemonOptions bind-mounts at /root/.cache/telepresence inside
// the container, alongside TELEPRESENCE_UID/TELEPRESENCE_GID env vars that
// tell the containerized daemon which host identity to chown its writes to
// (pkg/dos/filesystem.go's newOS). This file is linux-only (build-tagged)
// because of the unix.Stat_t ownership check below, matching this suite's
// own On("linux") registration.
type CacheFiles struct {
	rt.Suite
}

func init() {
	rt.Register(&CacheFiles{},
		rt.InArea("docker"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// Test_DaemonWritesOwnedByInvokingUser connects a named docker daemon and
// forces it to persist its log level: pkg/client/logging/
// cached_timed_level.go's SetAndStoreTimedLevel, called from inside the
// userd process itself, writes "<cache>/userd.loglevel". The resulting
// host-side file's owner UID must be the invoking user's, proving
// TELEPRESENCE_UID/GID took effect across the container boundary.
func (s *CacheFiles) Test_DaemonWritesOwnedByInvokingUser() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect(rt.ConnNamed(cacheFilesConnName), rt.ConnDocker())
	defer conn.Disconnect(t)

	cacheDir := filelocation.AppUserCacheDir(ctx)
	lvPath := filepath.Join(cacheDir, client.UserDaemonName+".loglevel")
	_ = os.Remove(lvPath) // start clean: a stale file from an earlier run would trivially pass.

	_, stderr, err := s.CLI().Run(ctx, "loglevel", "trace", "--use", cacheFilesConnName)
	s.Require().NoError(err, "loglevel: %s", stderr)
	defer func() {
		_, _, _ = s.CLI().Run(ctx, "loglevel", "debug", "--use", cacheFilesConnName)
	}()

	check.EventuallyFile(t, lvPath, func([]byte) bool { return true }, cacheFileTimeout)

	var st unix.Stat_t
	s.Require().NoError(unix.Stat(lvPath, &st))
	s.Equal(uint32(os.Getuid()), st.Uid, "cache file %s should be owned by the invoking user", lvPath)
}

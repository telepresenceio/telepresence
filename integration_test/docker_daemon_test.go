package integration_test

import (
	"context"
	"path/filepath"
	goRuntime "runtime"
	"strings"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

type dockerDaemonSuite struct {
	itest.Suite
	itest.NamespacePair
	ctx context.Context
}

func (s *dockerDaemonSuite) SuiteName() string {
	return "DockerDaemon"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &dockerDaemonSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *dockerDaemonSuite) SetupSuite() {
	if s.IsCI() && !(goRuntime.GOOS == "linux" && goRuntime.GOARCH == "amd64") {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
		return
	}
	s.Suite.SetupSuite()
	s.ctx = itest.WithConfig(s.HarnessContext(), func(cfg client.Config) {
		cfg.Intercept().UseFtp = false
	})
}

func (s *dockerDaemonSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *dockerDaemonSuite) Context() context.Context {
	return itest.WithT(s.ctx, s.T())
}

func (s *dockerDaemonSuite) Test_DockerDaemon_status() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--docker")

	status := itest.TelepresenceStatusOk(ctx)
	ud := status.UserDaemon
	s.True(ud.Running)
	s.True(strings.HasSuffix(ud.Name, s.AppNamespace()+"-cn"), "ends with suffix <namespace>-cn")
	s.Equal(ud.Status, "Connected")
}

func (s *dockerDaemonSuite) Test_DockerDaemon_hostDaemonNoConflict() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	_, _, err := itest.Telepresence(ctx, "connect", "--docker", "--namespace", s.AppNamespace(), "--manager-namespace", s.ManagerNamespace())
	s.NoError(err)
}

func (s *dockerDaemonSuite) Test_DockerDaemon_daemonHostNotConflict() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--docker")
	s.TelepresenceConnect(ctx)
}

func (s *dockerDaemonSuite) Test_DockerDaemon_cacheFiles() {
	ctx := s.Context()
	rq := s.Require()
	cache := filelocation.AppUserCacheDir(ctx)

	// Create a random file, just to get a dos-file handle with our own UID/GID
	rf, err := dos.Create(ctx, filepath.Join(s.T().TempDir(), "random.file"))
	rq.NoError(err)
	rs, err := logging.FStat(rf)
	_ = rf.Close()
	rq.NoError(err)

	lv := filepath.Join(cache, userd.ProcessName+".loglevel")
	ctx = dos.WithLockedFs(ctx)
	_ = dos.Remove(ctx, lv)
	s.TelepresenceConnect(ctx, "--docker")
	itest.TelepresenceOk(ctx, "loglevel", "trace")
	defer itest.TelepresenceOk(ctx, "loglevel", "debug")
	df, err := dos.Open(ctx, lv)
	rq.NoError(err)
	st, err := logging.FStat(df)
	_ = df.Close()
	rq.NoError(err)
	rq.True(st.HaveSameOwnerAndGroup(rs))
}

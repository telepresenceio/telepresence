package integration_test

import (
	"bufio"
	"context"
	"io"
	"os"
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

func (s *dockerDaemonSuite) Test_DockerDaemon_alsoProxy32() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--docker", "--also-proxy", "169.254.169.254/32", "--name", "a")
	itest.TelepresenceOk(ctx, "loglevel", "trace")
	defer itest.TelepresenceOk(ctx, "loglevel", "debug")

	rq := s.Require()
	logFile := filepath.Join(filelocation.AppUserLogDir(s.Context()), "connector.log")
	rootLog, err := os.Open(logFile)
	rq.NoError(err)
	defer rootLog.Close()

	// Figure out where the current end of the logfile is. This must be done before any
	// of the tests run because the queries that the DNS resolver receives are dependent
	// on how the system's DNS resolver handle search paths and caching.
	st, err := rootLog.Stat()
	rq.NoError(err)
	pos := st.Size()

	// Make an attempt to curl the also-proxied IP.
	_, _ = itest.Output(ctx, "docker", "run", "--network", "container:tp-a", "--rm", "curlimages/curl", "--silent", "--max-time", "1", "169.254.169.254")

	// Verify that the attempt is visible in the root log.
	_, err = rootLog.Seek(pos, io.SeekStart)
	rq.NoError(err)
	scn := bufio.NewScanner(rootLog)
	found := false

	// mustHaveWanted caters for cases where the default behavior from the system's resolver
	// is to not send unwanted queries to our resolver at all (based on search and routes).
	// It is forced to true for inclusion tests.
	for scn.Scan() {
		txt := scn.Text()
		if strings.Contains(txt, "169.254.169.254:80, code STREAM_INFO") {
			found = true
			break
		}
	}
	s.Truef(found, "Unable to find %q", "169.254.169.254:80, code STREAM_INFO")
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

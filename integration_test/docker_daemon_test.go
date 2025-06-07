package integration_test

import (
	"bufio"
	"context"
	"fmt"
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
	itest.TrafficManager
	ctx context.Context
}

func (s *dockerDaemonSuite) SuiteName() string {
	return "DockerDaemon"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &dockerDaemonSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
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
	const ipToTest = "10.10.74.1"
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--docker", "--also-proxy", ipToTest+"/32", "--name", "ax")
	itest.TelepresenceOk(ctx, "loglevel", "trace")
	defer itest.TelepresenceOk(ctx, "loglevel", "debug")

	rq := s.Require()
	logFile := filepath.Join(filelocation.AppUserLogDir(s.Context()), "connector.log")
	rootLog, err := os.Open(logFile)
	rq.NoError(err)
	defer rootLog.Close()

	// Figure out where the current end of the logfile is. This must be done before any
	// of the tests run because the queries that the DNS resolver receives are dependent
	// on how the system's DNS resolver handles search paths and caching.
	st, err := rootLog.Stat()
	rq.NoError(err)
	pos := st.Size()

	// Make an attempt to curl the also-proxied IP. The attempt will fail (there's nothing at the
	// other end), and that's OK. We're just interested in seeing it logged.
	_, _, _ = itest.Telepresence(ctx, "curl", "--silent", "--max-time", "1", ipToTest) //nolint:dogsled // X

	// Verify that the attempt is visible in the root log.
	_, err = rootLog.Seek(pos, io.SeekStart)
	rq.NoError(err)
	scn := bufio.NewScanner(rootLog)
	found := false

	// mustHaveWanted caters for cases where the default behavior from the system's resolver
	// is to not send unwanted queries to our resolver at all (based on search and routes).
	// It is forced to true for inclusion tests.
	strToFind := fmt.Sprintf("%s:80, code STREAM_INFO", ipToTest)
	for scn.Scan() {
		txt := scn.Text()
		if strings.Contains(txt, strToFind) {
			found = true
			break
		}
	}
	s.Truef(found, "Unable to find %q", strToFind)
}

func (s *dockerDaemonSuite) Test_DockerDaemon_daemonHostNotConflict() {
	ctx := s.Context()
	s.TelepresenceConnect(ctx, "--docker")
	s.TelepresenceConnect(ctx)
}

func (s *dockerDaemonSuite) Test_DockerDaemon_singleNameLookup() {
	ctx := s.Context()
	const svc = "echo-easy"
	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)
	out := s.TelepresenceConnect(ctx, "--docker", "--", itest.GetExecutable(ctx), "curl", "--silent", "--max-time", "1", svc)
	s.Contains(out, "Request served by "+svc)
	so, err := itest.TelepresenceStatus(ctx)
	s.NoError(err)
	s.Nil(so.ContainerizedDaemon)
	s.False(so.UserDaemon.Running)
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

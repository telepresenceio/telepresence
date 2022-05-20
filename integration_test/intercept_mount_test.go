package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	goRuntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type interceptMountSuite struct {
	itest.Suite
	itest.SingleService
	mountPoint  string
	cancelLocal context.CancelFunc
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) suite.TestingSuite {
		return &interceptMountSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}

func (s *interceptMountSuite) SetupSuite() {
	s.Suite.SetupSuite()
	// TempDir() will not be a valid mount on Windows -- it wants a lettered drive.
	if goRuntime.GOOS == "windows" {
		s.mountPoint = "T:"
	} else {
		var err error
		s.mountPoint, err = os.MkdirTemp("", "mount-") // Don't use the testing.Tempdir() because deletion is delayed.
		s.Require().NoError(err)
	}
	ctx := s.Context()
	var port int
	port, s.cancelLocal = itest.StartLocalHttpEchoServer(ctx, s.ServiceName())
	stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.AppNamespace(), s.ServiceName(), "--mount", s.mountPoint, "--port", strconv.Itoa(port))
	s.Contains(stdout, "Using Deployment "+s.ServiceName())
	s.CapturePodLogs(ctx, "app=echo", "traffic-agent", s.AppNamespace())
}

func (s *interceptMountSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "leave", fmt.Sprintf("%s-%s", s.ServiceName(), s.AppNamespace()))
	s.cancelLocal()
	s.Eventually(func() bool {
		stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
		return !strings.Contains(stdout, s.ServiceName()+": intercepted")
	}, 10*time.Second, time.Second)

	if goRuntime.GOOS != "windows" {
		// Delay the deletion of the mount point so that it is properly unmounted before it's removed.
		go func() {
			time.Sleep(2 * time.Second)
			_ = os.RemoveAll(s.mountPoint)
		}()
	}
}

func (s *interceptMountSuite) Test_InterceptMount() {
	if runtime.GOOS == "darwin" {
		s.T().Skip("Mount tests don't run on darwin due to macFUSE issues")
	}
	require := s.Require()
	ctx := s.Context()

	stdout := itest.TelepresenceOk(ctx, "--namespace", s.AppNamespace(), "list", "--intercepts")
	s.Regexp(s.ServiceName()+`\s*: intercepted`, stdout)

	time.Sleep(200 * time.Millisecond) // List is really fast now, so give the mount some time to become effective
	st, err := os.Stat(s.mountPoint)
	require.NoError(err, "Stat on <mount point> failed")
	require.True(st.IsDir(), "Mount point is not a directory")
	st, err = os.Stat(filepath.Join(s.mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}

func (s *singleServiceSuite) Test_InterceptMountRelative() {
	if runtime.GOOS == "darwin" {
		s.T().Skip("Mount tests don't run on darwin due to macFUSE issues")
	}
	if runtime.GOOS == "windows" {
		s.T().Skip("Windows mount on driver letters. Relative mounts are not possible")
	}
	require := s.Require()

	ctx := s.Context()
	port, cancel := itest.StartLocalHttpEchoServer(ctx, s.ServiceName())
	defer cancel()

	nwd, err := os.MkdirTemp("", "mount-") // Don't use the testing.Tempdir() because deletion is delayed.
	require.NoError(err)
	ctx = itest.WithWorkingDir(ctx, nwd)
	stdout := itest.TelepresenceOk(ctx,
		"intercept", "--namespace", s.AppNamespace(), s.ServiceName(), "--mount", "rel-dir", "--port", strconv.Itoa(port))
	defer func() {
		itest.TelepresenceOk(ctx, "leave", fmt.Sprintf("%s-%s", s.ServiceName(), s.AppNamespace()))
	}()
	s.Contains(stdout, "Using Deployment "+s.ServiceName())

	stdout = itest.TelepresenceOk(ctx, "--namespace", s.AppNamespace(), "list", "--intercepts")
	s.Regexp(s.ServiceName()+`\s*: intercepted`, stdout)

	time.Sleep(200 * time.Millisecond) // List is really fast now, so give the mount some time to become effective
	mountPoint := filepath.Join(nwd, "rel-dir")
	st, err := os.Stat(mountPoint)
	require.NoError(err, "Stat on <mount point> failed")
	require.True(st.IsDir(), "Mount point is not a directory")
	st, err = os.Stat(filepath.Join(mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}

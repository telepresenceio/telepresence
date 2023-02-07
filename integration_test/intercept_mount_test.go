package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	goRuntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
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
	switch runtime.GOOS {
	case "darwin":
		// Run without mounting on darwin. Apple prevents proper install of kernel extensions
		s.mountPoint = "false"
	case "windows":
		s.mountPoint = "T:"
	default:
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

	if goRuntime.GOOS != "windows" && goRuntime.GOOS != "darwin" {
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

func (s *singleServiceSuite) Test_InterceptDetailedOutput() {
	ctx := s.Context()
	port, cancel := itest.StartLocalHttpEchoServer(ctx, s.ServiceName())
	defer cancel()
	stdout := itest.TelepresenceOk(ctx, "intercept",
		"--namespace", s.AppNamespace(),
		"--mount", "false",
		"--port", strconv.Itoa(port),
		"--detailed-output",
		"--output", "json",
		s.ServiceName())
	defer func() {
		itest.TelepresenceOk(ctx, "leave", fmt.Sprintf("%s-%s", s.ServiceName(), s.AppNamespace()))
	}()
	var iInfo intercept.Info
	require := s.Require()
	require.NoError(json.Unmarshal([]byte(stdout), &iInfo))
	s.Equal(iInfo.Name, fmt.Sprintf("%s-%s", s.ServiceName(), s.AppNamespace()))
	s.Equal(iInfo.Disposition, "ACTIVE")
	s.Equal(iInfo.WorkloadKind, "Deployment")
	s.Equal(iInfo.TargetPort, int32(port))
	s.Equal(iInfo.Environment["TELEPRESENCE_CONTAINER"], "echo-server")
	m := iInfo.Mount
	require.NotNil(m)
	s.NotNil(iputil.Parse(m.PodIP))
	s.NotZero(m.Port)
	s.Equal(agentconfig.ExportsMountPoint+"/echo-server", m.RemoteDir)
	require.Len(m.Mounts, 1)
	s.Equal(m.Mounts[0], "/var/run/secrets/kubernetes.io")
}

func (s *singleServiceSuite) Test_NoInterceptorResponse() {
	if runtime.GOOS == "darwin" {
		s.T().Skip("Mount tests don't run on darwin due to macFUSE issues")
	}
	if runtime.GOOS == "windows" {
		s.T().Skip("Windows mount on driver letters. Relative mounts are not possible")
	}
	time.Sleep(2000 * time.Millisecond) // List is really fast now, so give the mount some time to become effective
	require := s.Require()

	ctx := s.Context()

	nwd, err := os.MkdirTemp("", "mount-") // Don't use the testing.Tempdir() because deletion is delayed.
	require.NoError(err)
	ctx = itest.WithWorkingDir(ctx, nwd)
	stdout := itest.TelepresenceOk(ctx,
		"intercept", "--namespace", s.AppNamespace(), s.ServiceName(), "--mount", "rel-dir", "--port", "8443")
	defer func() {
		itest.TelepresenceOk(ctx, "leave", fmt.Sprintf("%s-%s", s.ServiceName(), s.AppNamespace()))
	}()
	s.Contains(stdout, "Using Deployment "+s.ServiceName())
	stdout = itest.TelepresenceOk(ctx, "--namespace", s.AppNamespace(), "list", "--intercepts")
	s.Regexp(s.ServiceName()+`\s*: intercepted`, stdout)

	time.Sleep(2000 * time.Millisecond) // List is really fast now, so give the mount some time to become effective
	s.CapturePodLogs(ctx, "app="+s.ServiceName(), "traffic-agent", s.AppNamespace())

	mountPoint := filepath.Join(nwd, "rel-dir")
	st, err := os.Stat(mountPoint)
	require.NoError(err, "Stat on <mount point> failed")
	require.True(st.IsDir(), "Mount point is not a directory")
	st, err = os.Stat(filepath.Join(mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")

	// Bombard the echo service with lots of traffic. It's intercepted and will redirect the
	// traffic to the interceptor, but there's no such process listening. This must not
	// result in stream congestion that kills the intercept.
	url := "http://" + s.ServiceName()
	for i := 0; i < 1000; i++ {
		hc := http.Client{Timeout: 100 * time.Millisecond}
		resp, err := hc.Get(url)
		if err == nil {
			resp.Body.Close()
		}
	}

	// Verify that we still have a functional mount
	st, err = os.Stat(mountPoint)
	require.NoError(err, "Stat on <mount point> failed")
	require.True(st.IsDir(), "Mount point is not a directory")
	st, err = os.Stat(filepath.Join(mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}

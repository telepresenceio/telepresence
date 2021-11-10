package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	goRuntime "runtime"
	"strconv"
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
	// TempDir() will not be a valid mount on windows -- it wants a lettered drive.
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
}

func (s *interceptMountSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "leave", fmt.Sprintf("%s-%s", s.ServiceName(), s.AppNamespace()))
	s.cancelLocal()
	// Delay the deletion of the mount point so that it is properly unmounted before it's removed.
	go func() {
		time.Sleep(2 * time.Second)
		_ = os.RemoveAll(s.mountPoint)
	}()
}

func (s *interceptMountSuite) Test_InterceptMount() {
	require := s.Require()
	ctx := s.Context()

	stdout := itest.TelepresenceOk(ctx, "--namespace", s.AppNamespace(), "list", "--intercepts")
	s.Regexp(s.ServiceName()+`\s*: intercepted`, stdout)

	st, err := os.Stat(s.mountPoint)
	require.NoError(err, "Stat on <mount point> failed")
	require.True(st.IsDir(), "Mount point is not a directory")
	st, err = os.Stat(filepath.Join(s.mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}

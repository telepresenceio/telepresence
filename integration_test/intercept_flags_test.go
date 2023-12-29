package integration_test

import (
	"regexp"
	"strconv"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type interceptFlagSuite struct {
	itest.Suite
	itest.SingleService
}

func (s *interceptFlagSuite) SuiteName() string {
	return "InterceptFlag"
}

func init() {
	itest.AddSingleServiceSuite("-intercept-flag", "echo", func(h itest.SingleService) itest.TestingSuite {
		return &interceptFlagSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}

func (s *interceptFlagSuite) SetupSuite() {
	if s.CompatVersion() != "" {
		s.T().Skip("Not part of compatibility suite")
	}
	s.Suite.SetupSuite()
}

func (s *interceptFlagSuite) Test_ContainerReplace() {
	ctx := s.Context()
	require := s.Require()
	iceptName := "container-replaced"
	port, cancel := itest.StartLocalHttpEchoServer(ctx, iceptName)
	defer cancel()
	expectedOutput := regexp.MustCompile(iceptName + ` from intercept at`)

	stdout := itest.TelepresenceOk(ctx, "intercept", "--replace", "--port", strconv.Itoa(port), s.ServiceName())
	defer func() {
		itest.TelepresenceOk(ctx, "leave", s.ServiceName())
		s.Eventually(func() bool {
			out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "1", s.ServiceName())
			if err != nil {
				dlog.Error(ctx, err)
				return false
			}
			dlog.Info(ctx, out)
			return !expectedOutput.MatchString(out)
		}, 1*time.Minute, 6*time.Second)
		require.Contains(stdout, "1/1")
	}()
	require.Contains(stdout, "Using Deployment "+s.ServiceName())

	require.Eventually(func() bool {
		out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "1", s.ServiceName())
		if err != nil {
			dlog.Error(ctx, err)
			return false
		}
		dlog.Info(ctx, out)
		return expectedOutput.MatchString(out)
	}, 1*time.Minute, 6*time.Second)

	stdout, err := itest.KubectlOut(ctx, s.AppNamespace(), "get", "pod", "-lapp="+s.ServiceName())
	require.NoError(err)
	require.NotContains(stdout, "2/2")
	require.Contains(stdout, "1/1")
}

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type interceptFlagSuite struct {
	itest.Suite
	itest.NamespacePair
	serviceName string
}

func (s *interceptFlagSuite) SuiteName() string {
	return "InterceptFlag"
}

func init() {
	itest.AddTrafficManagerSuite("-intercept-flag", func(h itest.NamespacePair) itest.TestingSuite {
		return &interceptFlagSuite{Suite: itest.Suite{Harness: h}, NamespacePair: h}
	})
}

func (s *interceptFlagSuite) SetupSuite() {
	if s.IsCI() && runtime.GOOS == "darwin" {
		s.T().Skip("Mount tests don't run on darwin due to macFUSE issues")
		return
	}
	if s.CompatVersion() != "" {
		s.T().Skip("Not part of compatibility suite")
	}
	s.Suite.SetupSuite()
	ctx := s.Context()
	s.serviceName = "hello"
	s.KubectlOk(ctx, "create", "serviceaccount", testIamServiceAccount)
	s.ApplyApp(ctx, "hello-w-volumes", "deploy/"+s.serviceName)
	s.TelepresenceConnect(ctx)
}

func (s *interceptFlagSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	s.DeleteSvcAndWorkload(ctx, "deploy", s.serviceName)
	s.KubectlOk(ctx, "delete", "serviceaccount", testIamServiceAccount)
}

// Test_ContainerReplace tests that:
//
//   - Two containers in a pod can be intercepted in sequence. One with replace, and one without.
//   - Containers can be intercepted  interchangeably with or without --replace
//   - Volumes are mounted
//   - Intercept responses are produced from the intercept handlers
//   - Responses after the intercepts end are from the cluster
func (s *interceptFlagSuite) Test_ContainerReplace() {
	ctx := s.Context()

	const (
		n1 = "container_replaced"
		c1 = "hello-container-1"
		n2 = "container_kept"
		c2 = "hello-container-2"
	)

	localPort1, cancel1 := itest.StartLocalHttpEchoServer(ctx, n1)
	defer cancel1()

	localPort2, cancel2 := itest.StartLocalHttpEchoServer(ctx, n2)
	defer cancel2()

	tests := []struct {
		name         string
		iceptName    string
		appContainer string
		replace      bool
		localPort    int
		port         uint16
	}{
		{
			name:         n1,
			iceptName:    n1,
			appContainer: c1,
			replace:      true,
			localPort:    localPort1,
			port:         80,
		},
		{
			name:         n2,
			iceptName:    n2,
			appContainer: c2,
			replace:      false,
			localPort:    localPort2,
			port:         81,
		},
		{
			name:         "container_replace_kept",
			iceptName:    n1,
			appContainer: c1,
			replace:      false,
			localPort:    localPort1,
			port:         80,
		},
		{
			name:         "container_kept_replaced",
			iceptName:    n2,
			appContainer: c2,
			replace:      true,
			localPort:    localPort2,
			port:         81,
		},
	}

	for _, tt := range tests {
		tt := tt
		s.Run(tt.name, func() {
			ctx := s.Context()
			expectedOutput := regexp.MustCompile(tt.iceptName + ` from intercept at`)
			args := []string{"intercept"}
			if tt.replace {
				args = append(args, "--replace")
			}
			args = append(args, "--port", fmt.Sprintf("%d:%d", tt.localPort, tt.port), "--output", "json", "--detailed-output", "--workload", s.serviceName, tt.iceptName)
			jsOut := itest.TelepresenceOk(ctx, args...)
			agentCaptureCtx, agentCaptureCancel := context.WithCancel(ctx)
			s.CapturePodLogs(agentCaptureCtx, s.serviceName, "traffic-agent", s.AppNamespace())

			defer func() {
				agentCaptureCancel()
				itest.TelepresenceOk(ctx, "leave", tt.iceptName)
				s.CapturePodLogs(ctx, s.serviceName, tt.appContainer, s.AppNamespace())
				s.Eventually(func() bool {
					out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "1", iputil.JoinHostPort(s.serviceName, tt.port))
					if err != nil {
						dlog.Error(ctx, err)
						return false
					}
					if !expectedOutput.MatchString(out) {
						return true
					}
					dlog.Info(ctx, out)
					return false
				}, 1*time.Minute, 6*time.Second)
			}()

			var ii intercept.Info
			require := s.Require()
			require.NoError(json.Unmarshal([]byte(jsOut), &ii))
			require.Equal(ii.Name, tt.iceptName)

			// Ensure that all directories are mounted.
			require.NotNil(ii.Mount)
			mounts := ii.Mount.Mounts
			require.True(len(mounts) > 2)
			dlog.Infof(ctx, "Mounts = %v", mounts)
			for _, mount := range mounts {
				st, err := os.Stat(filepath.Join(ii.Mount.LocalDir, mount))
				require.NoError(err)
				require.True(st.IsDir())
			}

			require.Eventually(func() bool {
				out, err := itest.Output(ctx, "curl", "--silent", "--max-time", "1", iputil.JoinHostPort(s.serviceName, tt.port))
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				if expectedOutput.MatchString(out) {
					return true
				}
				dlog.Info(ctx, out)
				return false
			}, 1*time.Minute, 6*time.Second)
		})
	}
}

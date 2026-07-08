package integration_test

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/intercept"
)

func (s *mountsSuite) Test_IgnoredMounts() {
	type lm struct {
		name        string
		svcPort     int
		ignored     []string
		expected    []string
		notExpected []string
	}
	tests := []lm{
		{
			"no ingored volumes",
			80,
			[]string{},
			[]string{
				"var/run/secrets/kubernetes.io/serviceaccount",
				"var/run/secrets/datawire.io/auth",
				"usr/share/nginx/html",
				"etc/nginx/templates",
			},
			[]string{},
		},
		{
			"ignore-by-name",
			80,
			[]string{
				"hello-data-volume-1",
				"nginx-config",
			},
			[]string{
				"var/run/secrets/kubernetes.io/serviceaccount",
				"var/run/secrets/datawire.io/auth",
			},
			[]string{
				"usr/share/nginx/html",
				"etc/nginx/templates",
			},
		},
	}

	localPort, cancel := itest.StartLocalHttpEchoServer(s.Context(), "hello")
	defer cancel()

	for _, tt := range tests {
		s.Run(tt.name, func() {
			tpl := struct {
				Annotations map[string]string
			}{
				Annotations: map[string]string{
					annotation.InjectIgnoreVolumeMounts: strings.Join(tt.ignored, ","),
				},
			}
			ctx := s.Context()
			require := s.Require()
			s.ApplyTemplate(ctx, filepath.Join("testdata", "k8s", "hello-w-volumes.goyaml"), &tpl)
			defer s.DeleteSvcAndWorkload(ctx, "deploy", "hello")

			// Wait for the rollout before intercepting. The nginx image may
			// need to be pulled, and the intercept evicts the pod to inject
			// the traffic-agent, so intercepting a deployment whose first pod
			// is still starting puts two pod startups and an image pull
			// inside the intercept timeout.
			require.NoError(itest.RolloutStatusWait(ctx, s.AppNamespace(), "deploy/hello"))

			stdout := itest.TelepresenceOk(ctx, "intercept", "hello", "--format", "json", "--detailed-output", "--port", fmt.Sprintf("%d:%d", localPort, tt.svcPort))
			defer itest.TelepresenceOk(ctx, "leave", "hello")
			var iInfo intercept.Info
			require.NoError(json.Unmarshal([]byte(stdout), &iInfo))
			s.CapturePodLogs(ctx, "hello", "traffic-agent", s.AppNamespace())
			mountPoint := iInfo.Mount.LocalDir
			// The intercept command returns before the FUSE/sftp mount is necessarily populated, so
			// wait for each expected path to appear rather than stat-ing it immediately.
			for _, desired := range tt.expected {
				require.Eventuallyf(func() bool {
					st, err := os.Stat(filepath.Join(mountPoint, desired))
					return err == nil && st.IsDir()
				}, 30*time.Second, 2*time.Second, "mount of %s should be successful", desired)
			}
			// The expected mounts are present, so the mount is established and the ignored volumes,
			// which are never mounted, must be absent.
			for _, notDesired := range tt.notExpected {
				st, err := os.Stat(filepath.Join(mountPoint, notDesired))
				if !s.Errorf(err, "mount of %s should not be successful", notDesired) {
					clog.Infof(ctx, "stat gave us %s %t %s", st.Name(), st.IsDir(), st.Mode())
				}
			}
		})
	}
}

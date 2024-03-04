package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
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
		tt := tt
		s.Run(tt.name, func() {
			tpl := struct {
				Annotations map[string]string
			}{
				Annotations: map[string]string{
					agentconfig.InjectIgnoreVolumeMounts: strings.Join(tt.ignored, ","),
				},
			}
			ctx := s.Context()
			s.ApplyTemplate(ctx, filepath.Join("testdata", "k8s", "hello-w-volumes.goyaml"), &tpl)
			defer func() {
				s.DeleteSvcAndWorkload(ctx, "deploy", "hello")
			}()
			require := s.Require()
			stdout := itest.TelepresenceOk(ctx, "intercept", "hello", "--output", "json", "--detailed-output", "--port", fmt.Sprintf("%d:%d", localPort, tt.svcPort))
			defer itest.TelepresenceOk(ctx, "leave", "hello")
			var iInfo intercept.Info
			require.NoError(json.Unmarshal([]byte(stdout), &iInfo))
			s.CapturePodLogs(ctx, "hello", "traffic-agent", s.AppNamespace())
			mountPoint := iInfo.Mount.LocalDir
			for _, desired := range tt.expected {
				st, err := os.Stat(filepath.Join(mountPoint, desired))
				if !s.NoErrorf(err, "mount of %s should be successful", desired) {
					s.T().FailNow()
				}
				require.True(st.IsDir())
			}
			for _, notDesired := range tt.notExpected {
				st, err := os.Stat(filepath.Join(mountPoint, notDesired))
				if !s.Errorf(err, "mount of %s should not be successful", notDesired) {
					dlog.Infof(ctx, "stat gave us %s %t %s", st.Name(), st.IsDir(), st.Mode())
				}
			}
		})
	}
}

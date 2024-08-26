package integration_test

import (
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

func (s *connectedSuite) Test_BindToPodIP() {
	const svcPfx = "echo-server"
	tplPath := filepath.Join("testdata", "k8s", "generic.goyaml")

	for i, targetPort := range []string{"8080", "http"} {
		s.Run("TargetPort:"+targetPort, func() {
			ctx := s.Context()
			svc := fmt.Sprintf("%s-%d", svcPfx, i)
			svcPort, cancel := itest.StartLocalHttpEchoServer(ctx, svc)
			defer cancel()
			tpl := &itest.Generic{
				Name:       svc,
				TargetPort: targetPort,
				Registry:   "ghcr.io/telepresenceio",
				Image:      "echo-server:latest",
				Environment: []core.EnvVar{
					{
						Name:  "PORTS",
						Value: "8080",
					},
					{
						Name: "LISTEN_ADDRESS",
						ValueFrom: &core.EnvVarSource{
							FieldRef: &core.ObjectFieldSelector{
								FieldPath: "status.podIP",
							},
						},
					},
				},
				Annotations: map[string]string{
					agentconfig.InjectAnnotation: "enabled",
				},
			}
			s.ApplyTemplate(ctx, tplPath, tpl)
			rq := s.Require()
			rq.NoError(s.RolloutStatusWait(ctx, "deploy/"+svc))

			defer s.DeleteTemplate(ctx, tplPath, tpl)

			stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", svc, "--port", strconv.Itoa(svcPort))
			rq.Contains(stdout, "Using Deployment "+svc)

			itest.PingInterceptedEchoServer(ctx, svc, "80")
			itest.TelepresenceOk(ctx, "leave", svc)

			// Ensure that we now reach the original app again.
			s.Eventually(func() bool {
				out, err := itest.Output(ctx, "curl", "--verbose", "--max-time", "0.5", svc)
				dlog.Infof(ctx, "Received %s", out)
				if err != nil {
					dlog.Errorf(ctx, "curl error %v", err)
					return false
				}
				return true
			}, 30*time.Second, 2*time.Second, "Pod app is not reachable after ending the intercept")
		})
	}
}

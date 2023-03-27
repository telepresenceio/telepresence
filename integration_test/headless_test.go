package integration_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/datawire/dlib/dcontext"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) Test_SuccessfullyInterceptsHeadlessService() {
	if itest.GetProfile(s.Context()) == itest.GkeAutopilotProfile {
		s.T().Skip("GKE Autopilot does not support NET_ADMIN containers which means headless services can't be intercepted")
	}
	ctx, cancel := context.WithCancel(dcontext.WithSoftness(s.Context()))
	defer cancel()
	const svc = "echo-headless"

	svcPort, svcCancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer svcCancel()

	s.ApplyApp(ctx, "echo-headless", "statefulset/echo-headless")
	defer s.DeleteSvcAndWorkload(ctx, "statefulset", "echo-headless")

	for _, test := range []struct {
		webhook bool
		name    string
	}{
		{
			webhook: true,
			name:    "injected from webhook",
		},
		{
			webhook: false,
			name:    "injected from command",
		},
	} {
		s.Run(test.name, func() {
			require := s.Require()
			ctx := s.Context()
			if test.webhook {
				require.NoError(annotateForWebhook(ctx, "statefulset", "echo-headless", s.AppNamespace(), 8080))
				require.Eventually(
					func() bool {
						stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--agents")
						return strings.Contains(stdout, "echo-headless: ready to intercept")
					},
					30*time.Second, // waitFor
					3*time.Second,  // polling interval
					`never gets install agent`)
			}
			stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", svc, "--port", strconv.Itoa(svcPort))
			require.Contains(stdout, "Using StatefulSet echo-headless")
			s.CapturePodLogs(ctx, "service=echo-headless", "traffic-agent", s.AppNamespace())

			defer func() {
				itest.TelepresenceOk(ctx, "leave", "echo-headless-"+s.AppNamespace())
				if test.webhook {
					require.NoError(dropWebhookAnnotation(ctx, "statefulset", "echo-headless", s.AppNamespace()))
				}

				// Switch to default user and uninstall the agent
				itest.TelepresenceQuitOk(ctx)
				dfltCtx := itest.WithUser(ctx, "default")
				itest.TelepresenceOk(dfltCtx, "uninstall", "--agent", "echo-headless", "-n", s.AppNamespace())
				itest.TelepresenceQuitOk(dfltCtx)
				itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())

				require.Eventually(
					func() bool {
						stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--agents")
						return !strings.Contains(stdout, "echo-headless")
					},
					30*time.Second, // waitFor
					3*time.Second,  // polling interval
					`agent is never removed`)
			}()

			require.Eventually(
				func() bool {
					stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
					return strings.Contains(stdout, "echo-headless: intercepted")
				},
				30*time.Second, // waitFor
				3*time.Second,  // polling interval
				`intercepted workload never show up in list`)

			itest.PingInterceptedEchoServer(ctx, svc, "8080")
		})
	}
}

func annotateForWebhook(ctx context.Context, objKind, objName, objNamespace string, servicePort int) error {
	err := itest.Kubectl(ctx, objNamespace, "patch", objKind, objName, "-p", fmt.Sprintf(`
{
	"spec": {
		"template": {
			"metadata": {
				"annotations": {
					"telepresence.getambassador.io/inject-traffic-agent": "enabled",
					"telepresence.getambassador.io/inject-service-port": "%d"
				}
			}
		}
	}
}`, servicePort))
	if err != nil {
		return err
	}
	return itest.RolloutStatusWait(ctx, objNamespace, objKind+"/"+objName)
}

func dropWebhookAnnotation(ctx context.Context, objKind, objName, objNamespace string) error {
	err := itest.Kubectl(ctx, objNamespace, "patch", objKind, objName, "--type=json", "-p", `[{
	"op": "remove",
	"path": "/spec/template/metadata/annotations/telepresence.getambassador.io~1inject-traffic-agent"
},
{
	"op": "remove",
	"path": "/spec/template/metadata/annotations/telepresence.getambassador.io~1inject-service-port"
}
]`)
	if err != nil {
		return err
	}
	return itest.RolloutStatusWait(ctx, objNamespace, objKind+"/"+objName)
}

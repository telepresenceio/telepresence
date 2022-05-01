package integration_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
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
				itest.TelepresenceOk(ctx, "uninstall", "--agent", "echo-headless", "-n", s.AppNamespace())
				require.Eventually(
					func() bool {
						stdout := itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--agents")
						return !strings.Contains(stdout, "echo-headless")
					},
					30*time.Second, // waitFor
					3*time.Second,  // polling interval
					`agent is never removed`)
			}()

			stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
			require.Contains(stdout, "echo-headless: intercepted")
			require.NotContains(stdout, "Volume Mount Point")

			expectedOutput := fmt.Sprintf("%s from intercept at /", svc)
			resolver := &net.Resolver{}
			require.Eventually(
				// condition
				func() bool {
					ips, err := resolver.LookupIP(ctx, "ip4", svc)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					if 1 != len(ips) {
						dlog.Infof(ctx, "Lookup for %s returned %v", svc, ips)
						return false
					}

					url := fmt.Sprintf("http://%s:8080", ips[0])

					dlog.Infof(ctx, "trying %q...", url)
					hc := http.Client{Timeout: 2 * time.Second}
					resp, err := hc.Get(url)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					defer resp.Body.Close()
					dlog.Infof(ctx, "status code: %v", resp.StatusCode)
					body, err := io.ReadAll(resp.Body)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					dlog.Infof(ctx, "body: %q", body)
					return string(body) == expectedOutput
				},
				time.Minute,   // waitFor
				3*time.Second, // polling interval
				`body of %q equals %q`, "http://"+svc, expectedOutput,
			)
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

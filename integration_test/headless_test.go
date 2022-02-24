package integration_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) Test_SuccessfullyInterceptsHeadlessService() {
	ctx, cancel := context.WithCancel(dcontext.WithSoftness(s.Context()))
	defer cancel()
	const svc = "echo-headless"

	svcPort, svcCancel := itest.StartLocalHttpEchoServer(ctx, svc)
	defer svcCancel()

	s.ApplyApp(ctx, "echo-headless", "statefulset/echo-headless")
	defer s.DeleteSvcAndWorkload(ctx, "statefulset", "echo-headless")

	require := s.Require()

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
			ctx := s.Context()
			if test.webhook {
				require.NoError(annotateForWebhook(ctx, "statefulset", "echo-headless", s.AppNamespace(), 8080))
			}
			stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", svc, "--port", strconv.Itoa(svcPort))
			require.Contains(stdout, "Using StatefulSet echo-headless")
			s.CapturePodLogs(ctx, "service=echo-headless", "traffic-agent", s.AppNamespace())

			defer func() {
				itest.TelepresenceOk(ctx, "leave", "echo-headless-"+s.AppNamespace())
				if test.webhook {
					require.NoError(dropWebhookAnnotation(ctx, "statefulset", "echo-headless", s.AppNamespace()))
					// Give the annotation drop some time to take effect, or the next run will often fail with a "the object has been modified" error
					dtime.SleepWithContext(ctx, 2*time.Second)
				} else {
					itest.TelepresenceOk(ctx, "uninstall", "--agent", "echo-headless", "-n", s.AppNamespace())
				}
			}()

			stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
			require.Contains(stdout, "echo-headless: intercepted")
			require.NotContains(stdout, "Volume Mount Point")

			expectedOutput := fmt.Sprintf("%s from intercept at /", svc)
			s.Require().Eventually(
				// condition
				func() bool {
					ip, err := net.DefaultResolver.LookupHost(ctx, svc)
					if err != nil {
						dlog.Infof(ctx, "%v", err)
						return false
					}
					if 1 != len(ip) {
						dlog.Infof(ctx, "Lookup for %s returned %v", svc, ip)
						return false
					}

					url := fmt.Sprintf("http://%s:8080", svc)

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

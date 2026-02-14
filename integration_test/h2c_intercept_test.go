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

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

type h2cInterceptSuite struct {
	itest.Suite
	itest.TrafficManager
}

func (s *h2cInterceptSuite) SuiteName() string {
	return "H2CIntercept"
}

func init() {
	itest.AddConnectedSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &h2cInterceptSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (s *h2cInterceptSuite) SetupSuite() {
	if !(s.ManagerIsVersion(">2.26.x") && s.ClientIsVersion(">2.26.x")) {
		s.T().Skip("H2C intercepts require Telepresence 2.27.0 or later")
	}
	s.Suite.SetupSuite()
}

// startLocalH2CEchoServer starts a local HTTP server that supports h2c (HTTP/2 cleartext)
// and echoes a line with the given name and the current URL path.
func startLocalH2CEchoServer(ctx context.Context, name string) (int, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp", "localhost:0")
	if err != nil {
		cancel()
		panic(fmt.Sprintf("failed to listen: %v", err))
	}
	pr := new(http.Protocols)
	pr.SetHTTP1(true)
	pr.SetUnencryptedHTTP2(true)
	sc := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "%s from intercept at %s", name, r.URL.Path)
		}),
		Protocols: pr,
	}
	go func() {
		_ = sc.Serve(l)
	}()
	go func() {
		<-ctx.Done()
		sdCtx, sdCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer sdCancel()
		_ = sc.Shutdown(sdCtx)
	}()
	return l.Addr().(*net.TCPAddr).Port, cancel
}

func (s *h2cInterceptSuite) Test_H2CInterceptPreservesProtocol() {
	ctx := s.Context()
	require := s.Require()

	const svc = "echo-h2c"

	// Deploy echo-server with appProtocol: kubernetes.io/h2c
	itest.ApplyAppTemplate(ctx, s.AppNamespace(), &itest.AppData{
		AppName: svc,
		Ports: []itest.AppPort{
			{
				ServicePortNumber: 80,
				TargetPortNumber:  8080,
				AppProtocol:       "kubernetes.io/h2c",
			},
		},
		Env: map[string]string{"PORTS": "8080:http"},
	})
	defer itest.DeleteSvcAndWorkload(ctx, "deploy", svc, s.AppNamespace())

	// Start a local h2c-capable echo server to receive intercepted traffic.
	// The upstream handler must support the same protocol as the app.
	localPort, cancel := startLocalH2CEchoServer(ctx, svc)
	defer cancel()

	// Create a personal HTTP intercept with a header filter
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", svc,
		"--http-header", "x-test=h2c",
		"--port", strconv.Itoa(localPort)+":80",
		"--mount=false")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Capture traffic-agent logs for debugging
	s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())

	// Verify non-intercepted traffic uses HTTP/2 (h2c).
	// The intercept is active so traffic goes through the agent's HTTP handler.
	// Requests without the x-test header don't match the intercept and are forwarded
	// by the default reverse proxy. The fix ensures that this proxy uses h2c prior
	// knowledge, so the echo-server sees HTTP/2.0.
	require.Eventually(func() bool {
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", svc)
		if err != nil {
			clog.Info(ctx, err)
			return false
		}
		ips = iputil.UniqueSorted(ips)
		if len(ips) != 1 {
			clog.Infof(ctx, "Lookup for %s returned %v", svc, ips)
			return false
		}
		hc := http.Client{Timeout: 2 * time.Second}
		rq, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s", net.JoinHostPort(ips[0].String(), "80")), nil)
		if err != nil {
			clog.Info(ctx, err)
			return false
		}
		resp, err := hc.Do(rq)
		if err != nil {
			clog.Info(ctx, err)
			return false
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			clog.Info(ctx, err)
			return false
		}
		r := string(body)
		clog.Infof(ctx, "non-intercepted response: %s", r)
		return strings.Contains(r, "HTTP/2.0 GET /")
	}, 30*time.Second, 3*time.Second, "expected HTTP/2.0 in non-intercepted response")

	// Verify intercepted traffic reaches the local h2c handler
	itest.PingInterceptedEchoServer(ctx, svc, "80", "x-test=h2c")

	// Leave the intercept
	_, _, err = itest.Telepresence(ctx, "leave", svc)
	require.NoError(err)
}

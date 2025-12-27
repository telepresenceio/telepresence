package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type httpInterceptsSuite struct {
	itest.Suite
	itest.SingleService
}

func (s *httpInterceptsSuite) SuiteName() string {
	return "HTTPIntercepts"
}

func (s *httpInterceptsSuite) SetupSuite() {
	if !(s.ManagerIsVersion(">2.24.x") && s.ClientIsVersion(">2.24.x")) {
		s.T().Skip("HTTP intercepts require Telepresence 2.25.0 or later")
	}
	s.Suite.SetupSuite()
}

func (s *httpInterceptsSuite) Test_HTTPHeaderFiltering() {
	require := s.Require()
	ctx := s.Context()

	// Test HTTP intercept with header filters using equals format
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--http-header", "X-User-ID=dev123", "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_HTTPHeaderFiltering_CurlFormat() {
	require := s.Require()
	ctx := s.Context()

	// Test HTTP intercept with header filters using colon format (curl -H compatible)
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--http-header", "X-User-ID: dev123", "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_HTTPPathFiltering() {
	require := s.Require()
	ctx := s.Context()

	// Test HTTP intercept with path filters
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--http-path-prefix", "/api/", "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_HTTPCombinedFiltering() {
	require := s.Require()
	ctx := s.Context()

	// Test HTTP intercept with both header and path filters, using mixed formats
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(),
		"--http-header", "X-User-ID=dev123",
		"--http-header", "Authorization: Bearer token123",
		"--http-path-prefix", "/api/",
		"--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_BackwardCompatibility() {
	require := s.Require()
	ctx := s.Context()

	// Test that standard TCP intercepts still work without HTTP filters
	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.ServiceName(), "--port", "8080")
	require.NoError(err, "stderr: %s", stderr)
	require.Contains(stdout, "Using Deployment")

	// Clean up
	_, _, err = itest.Telepresence(ctx, "leave", s.ServiceName())
	require.NoError(err)
}

func (s *httpInterceptsSuite) Test_HTTPInterceptCoexistence() {
	require := s.Require()
	ctx := s.Context()

	// This test verifies that multiple personal intercepts with different HTTP headers
	// can coexist on the same workload without conflicts (fixes issue #3969)

	// Start first personal intercept with x-user=adam
	stdout1, stderr1, err1 := itest.Telepresence(ctx, "intercept", "echo-one",
		"--workload", s.ServiceName(),
		"--http-header", "x-user=adam",
		"--port", "8080:80",
		"--mount", "false")
	require.NoError(err1, "First intercept failed - stderr: %s", stderr1)
	require.Contains(stdout1, "Using Deployment")

	// Start second personal intercept with x-user=bertil
	stdout2, stderr2, err2 := itest.Telepresence(ctx, "intercept", "echo-two",
		"--workload", s.ServiceName(),
		"--http-header", "x-user=bertil",
		"--port", "8081:80",
		"--mount", "false")
	require.NoError(err2, "Second intercept failed - stderr: %s", stderr2)
	require.Contains(stdout2, "Using Deployment")

	// Verify both intercepts are active and listed
	listOutput, listStderr, listErr := itest.Telepresence(ctx, "list", "--intercepts")
	require.NoError(listErr, "Failed to list intercepts - stderr: %s", listStderr)
	require.Contains(listOutput, "Intercept name: echo-one")
	require.Contains(listOutput, "Intercept name: echo-two")

	// Clean up both intercepts
	_, _, err3 := itest.Telepresence(ctx, "leave", "echo-one")
	require.NoError(err3, "Failed to leave first intercept")

	_, _, err4 := itest.Telepresence(ctx, "leave", "echo-two")
	require.NoError(err4, "Failed to leave second intercept")
}

func (s *httpInterceptsSuite) Test_TCPPortConflictDetection() {
	require := s.Require()
	ctx := s.Context()

	// This test verifies that real TCP port conflicts are still properly detected
	// when two intercepts try to use the same local port

	// Start first intercept using port 8080
	stdout1, stderr1, err1 := itest.Telepresence(ctx, "intercept", "tcp-conflict-one",
		"--workload", s.ServiceName(),
		"--http-header", "x-user=adam",
		"--port", "8080:80",
		"--mount", "false")
	require.NoError(err1, "First intercept should succeed - stderr: %s", stderr1)
	require.Contains(stdout1, "Using Deployment")

	// Attempt second intercept using the SAME local port 8080
	// This should fail with a TCP port conflict, not an agent intercept conflict
	_, stderr2, err2 := itest.Telepresence(ctx, "intercept", "tcp-conflict-two",
		"--workload", s.ServiceName(),
		"--http-header", "x-user=bertil",
		"--port", "8080:80", // Same local port as first intercept
		"--mount", "false")

	// Should fail due to real TCP port conflict
	require.Error(err2, "Second intercept should fail due to TCP port conflict")

	// Verify it's a TCP port binding error, not an agent intercept conflict
	require.Contains(stderr2, "127.0.0.1:8080", "Error should mention the conflicting local port")
	require.Contains(stderr2, "already in use", "Error should indicate port is already in use")

	// Should NOT contain agent intercept conflict message
	require.NotContains(stderr2, "Conflicts with", "Should not be an agent intercept conflict")

	// Clean up the successful intercept
	_, _, err3 := itest.Telepresence(ctx, "leave", "tcp-conflict-one")
	require.NoError(err3, "Failed to leave first intercept")
}

func init() {
	itest.AddSingleServiceSuite("", "echo", func(h itest.SingleService) itest.TestingSuite {
		return &httpInterceptsSuite{Suite: itest.Suite{Harness: h}, SingleService: h}
	})
}

func (s *httpInterceptsSuite) Test_HTTPManySimultaneous() {
	if _, ok := os.LookupEnv("HTTP_INTERCEPT_STRESS_TEST"); !ok {
		s.T().Skip("Run this stress manually. It's too demanding for the CI infrastructure.")
		return
	}
	require := s.Require()
	ctx := s.Context()

	const interceptCount = 250
	const pingRepeatCount = 10
	localPorts := make([]int, interceptCount)
	httpCancels := make([]context.CancelFunc, interceptCount)
	responseFunc := func(name string, r *http.Request) string {
		return fmt.Sprintf("%s, X-Personal-Id: %s, X-Repeat-Count: %s",
			name, r.Header.Get("X-Personal-Id"), r.Header.Get("X-Repeat-Count"))
	}

	// Ensure that a traffic-agent is running on the workload and capture its log
	itest.TelepresenceOk(ctx, "intercept", "--mount", "false", s.ServiceName())
	itest.TelepresenceOk(ctx, "leave", s.ServiceName())
	s.CapturePodLogs(ctx, s.ServiceName(), "traffic-agent", s.AppNamespace())

	for i := 0; i < interceptCount; i++ {
		id := strconv.Itoa(i)
		localPorts[i], httpCancels[i] = itest.StartLocalHttpEchoServerWithAddr(ctx, "echo-"+id, "localhost:0", responseFunc)
	}
	defer func() {
		for _, cancel := range httpCancels {
			cancel()
		}
	}()

	for i := 0; i < interceptCount; i++ {
		id := strconv.Itoa(i)
		hdr := "X-Personal-Id=" + id
		svc := "echo-" + id
		stdout, stderr, err := itest.Telepresence(ctx, "intercept", svc,
			"--workload", s.ServiceName(),
			"--http-header", hdr,
			"--port", strconv.Itoa(localPorts[i])+":80",
			"--mount", "false")
		require.NoError(err, "stderr: %s", stderr)
		require.Contains(stdout, "Using Deployment")
	}

	wg := &sync.WaitGroup{}
	wg.Add(interceptCount * pingRepeatCount)
	for i := 0; i < interceptCount; i++ {
		for n := 0; n < pingRepeatCount; n++ {
			go func(i int) {
				defer wg.Done()
				hdr := "X-Personal-Id=" + strconv.Itoa(i)
				rpt := "X-Repeat-Count=" + strconv.Itoa(n)
				expectedOutput := fmt.Sprintf("echo-%d, X-Personal-Id: %d, X-Repeat-Count: %d", i, i, n)
				itest.PingInterceptedEchoServerAndExpect(ctx, s.ServiceName(), "80", expectedOutput, hdr, rpt)
			}(i)
		}
	}
	wg.Wait()

	for i := 0; i < interceptCount; i++ {
		id := strconv.Itoa(i)
		_, _, err := itest.Telepresence(ctx, "leave", "echo-"+id)
		require.NoError(err, "Failed to leave intercept echo-"+id)
	}
}

func (s *notConnectedSuite) Test_HTTPManyClientsSimultaneous() {
	testHTTPManyClientsSimultaneous(s, "echo-easy", "/")
}

func (s *otelSuite) Test_OtelHTTPManyClientsSimultaneous() {
	testHTTPManyClientsSimultaneous(s, "echo-spring", "/rest/echo")
}

type NamespaceSuite interface {
	itest.NamespacePair
	T() *testing.T
	Context() context.Context
	Contains(actual any, expected any, msgAndArgs ...any) bool
	NoError(err error, msgAndArgs ...any) bool
	FailNow(msg string, args ...any) bool
	Eventually(f func() bool, timeout time.Duration, tick time.Duration, msgAndArgs ...any) bool
}

func testHTTPManyClientsSimultaneous(s NamespaceSuite, svc, path string) {
	if _, ok := os.LookupEnv("HTTP_INTERCEPT_STRESS_TEST"); !ok {
		s.T().Skip("Run this stress manually. It's too demanding for the CI infrastructure.")
		return
	}
	ctx := s.Context()

	// High values here will likely cause errors like "too many open files" unless the docker service is configured to allow more.
	// On a Linux box, this is typically done by adding a /etc/systemd/system/docker.service.d/override.conf file with the following contents:
	// [Service]
	// LimitNOFILE=infinity
	//
	// Also add the following line to /etc/docker/daemon.json:
	// {
	// 	"default-ulimits": {
	//		"nofile": {
	//			"Name": "nofile",
	//			"Soft": 64000,
	//			"Hard": 64000
	//		}
	//	}
	// }
	const interceptCount = 16

	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)
	s.TelepresenceConnect(ctx)
	itest.TelepresenceOk(ctx, "intercept", "--mount", "false", svc)
	itest.TelepresenceQuitOk(ctx)
	s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())

	conns := make([]string, 0, interceptCount)
	defer func() {
		for _, connName := range conns {
			_, _, err := itest.Telepresence(ctx, "--use", connName, "quit")
			s.NoError(err)
		}
	}()

	for i := 0; i < interceptCount; i++ {
		id := strconv.Itoa(i)
		connName := "conn-" + id + "-v"
		_, err := s.TelepresenceTryConnect(ctx, "--docker", "--name", connName)
		if err != nil {
			s.FailNow("Failed to connect to telepresence", err)
		}
		conns = append(conns, connName)
	}

	wg := &sync.WaitGroup{}
	wg.Add(interceptCount)
	for i := 0; i < interceptCount; i++ {
		id := strconv.Itoa(i)
		hdr := "X-Personal-Id=" + id
		connName := conns[i]
		go func() {
			defer wg.Done()
			tpCtx, cancel := context.WithCancel(ctx)
			outCh := make(chan string)
			go func() {
				defer close(outCh)
				stdout, stderr, err := itest.Telepresence(tpCtx, "--use", connName, "intercept", svc, "--mount=false", "--http-header", hdr, "--docker-run", "--port", "8080:80", "--", "--name", connName+".local", "telepresenceio/echo-server")
				s.NoError(err, "stderr: %s", stderr)
				outCh <- stdout
			}()
			s.Eventually(func() bool {
				so, se, err := itest.Telepresence(ctx, "--use", connName, "curl", "--silent", "--max-time", "2", "-H", "X-Personal-Id: "+id, svc+path)
				if err != nil {
					dlog.Error(ctx, so, se, err)
					return false
				}
				return strings.Contains(so, "Intercepted container")
			}, 10*time.Second, 1*time.Second)

			itest.TelepresenceOk(ctx, "--use", connName, "quit")
			cancel()
			<-outCh
		}()
	}
	wg.Wait()
}

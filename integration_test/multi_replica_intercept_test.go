package integration_test

import (
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type multiReplicaInterceptSuite struct {
	itest.Suite
	itest.TrafficManager
	svc       string
	localPort int
}

func (s *multiReplicaInterceptSuite) SuiteName() string {
	return "MultiReplicaIntercept"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &multiReplicaInterceptSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h, svc: "echo-4replicas"}
	})
}

func (s *multiReplicaInterceptSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	itest.ApplyApp(ctx, s.svc, s.AppNamespace(), "deploy/"+s.svc)
	s.Require().Eventually(func() bool {
		return len(itest.RunningPodNames(ctx, s.svc, s.AppNamespace())) == 4
	}, itest.PodCreateTimeout(ctx), 6*time.Second, "waiting for 4 running pods")

	// The upstream echo-server image supports h2c, so the traffic-agent will probe it
	// and forward intercepted requests via h2c. The local server must also support h2c.
	var cancel func()
	s.localPort, cancel = startLocalH2CEchoServer(ctx, s.svc)
	s.T().Cleanup(cancel)
}

func (s *multiReplicaInterceptSuite) TearDownSuite() {
	ctx := s.Context()
	itest.DeleteApp(ctx, s.svc, s.AppNamespace())
}

// Test_PersonalInterceptAllReplicasRouted is a regression test for #4085:
// personal HTTP intercepts must route all requests through all N replicas,
// not just the one pod whose IP was first written to InterceptInfo.
func (s *multiReplicaInterceptSuite) Test_PersonalInterceptAllReplicasRouted() {
	s.assertAllReplicasRouted("x-intercept-id=fix-4085")
}

// Test_GlobalInterceptAllReplicasRouted is a regression test for #4183: a
// global (unfiltered) intercept must route all requests through all N
// replicas too. Every agent redirects its own pod's traffic, so every agent
// pod needs a WatchDial connection from the client -- with only the pod
// recorded on the intercept getting one, traffic that the service
// load-balanced to the other replicas was redirected but never delivered.
func (s *multiReplicaInterceptSuite) Test_GlobalInterceptAllReplicasRouted() {
	s.assertAllReplicasRouted("")
}

// assertAllReplicasRouted intercepts s.svc (filtered on header when it is
// non-empty, globally otherwise) and requires that 100 concurrent requests,
// each on its own connection so the service load-balances them independently
// across the 4 replicas, all return the local server's response.
func (s *multiReplicaInterceptSuite) assertAllReplicasRouted(header string) {
	require := s.Require()
	ctx := s.Context()

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	args := []string{
		"intercept", s.svc,
		"--port", fmt.Sprintf("%d:80", s.localPort),
		"--mount=false",
	}
	if header != "" {
		args = append(args, "--http-header", header)
	}
	stdout, stderr, err := itest.Telepresence(ctx, args...)
	require.NoError(err, "stdout: %s\nstderr: %s", stdout, stderr)
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "leave", s.svc)
	}()

	// The intercept triggers a rolling restart that injects the traffic-agent
	// sidecar into every replica. Wait for that rollout to complete before sending
	// any traffic — otherwise some requests would land on stale pods that have no
	// agent and time out, masking the actual multi-replica routing behaviour.
	require.NoError(itest.RolloutStatusWait(ctx, s.AppNamespace(), "deploy/"+s.svc))
	require.Eventually(func() bool {
		pods := itest.RunningPodNames(ctx, s.svc, s.AppNamespace())
		return len(pods) == 4
	}, 60*time.Second, 2*time.Second, "waiting for all 4 agent-injected pods to be running")

	s.CapturePodLogs(ctx, s.svc, "traffic-agent", s.AppNamespace())

	// Wait for the intercept to become ACTIVE
	require.Eventually(func() bool {
		out, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		if err != nil {
			return false
		}
		return s.Contains(out, "ACTIVE")
	}, 30*time.Second, 2*time.Second)

	// Allow a short settling period for all agent pods to register with the intercept.
	time.Sleep(5 * time.Second)

	// Fire 100 concurrent requests. Each request opens a fresh TCP connection
	// (keep-alives disabled) so it is independently load-balanced across the
	// replicas. Without a WatchDial connection from the client, an agent on
	// pods B/C/D redirects its share of the requests but cannot deliver them.
	// A small random jitter staggers the requests so they don't all hit the
	// service in lock-step.
	expect := s.svc + " from intercept at /"
	hc := http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	var failures atomic.Int64
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(rand.IntN(500)) * time.Millisecond)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.svc, nil)
			if err != nil {
				clog.Infof(ctx, "request %d: build error: %v", i, err)
				failures.Add(1)
				return
			}
			if header != "" {
				kv := strings.SplitN(header, "=", 2)
				req.Header.Set(kv[0], kv[1])
			}
			resp, err := hc.Do(req)
			if err != nil {
				clog.Infof(ctx, "request %d: error: %v", i, err)
				failures.Add(1)
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != expect {
				clog.Infof(ctx, "request %d: unexpected body: %q", i, string(body))
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(failures.Load(), "%d out of 100 requests did not return the intercepted response", failures.Load())
}

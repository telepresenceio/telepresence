package integration_test

import (
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
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
	require := s.Require()
	ctx := s.Context()

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	stdout, stderr, err := itest.Telepresence(ctx, "intercept", s.svc,
		"--http-header", "x-intercept-id=fix-4085",
		"--port", fmt.Sprintf("%d:80", s.localPort),
		"--mount=false",
	)
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

	// Fire 100 concurrent requests with the intercept header. Each request opens a
	// fresh TCP connection (keep-alives disabled) so it is independently
	// load-balanced across the replicas. Before the fix, ~75% of requests would
	// fail because agents on pods B/C/D lack a WatchDial connection back to the
	// client. A small random jitter staggers the requests so they don't all hit
	// the service in lock-step.
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
			req.Header.Set("x-intercept-id", "fix-4085")
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

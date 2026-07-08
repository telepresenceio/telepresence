package integration_test

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type wiretapSuite struct {
	itest.Suite
	itest.TrafficManager
	svc      string
	tplPath  string
	tpl      *itest.Generic
	hitCount int32
}

func (s *wiretapSuite) SuiteName() string {
	return "Wiretap"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &wiretapSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h, svc: "echo-wt"}
	})
}

func (s *wiretapSuite) SetupSuite() {
	if !(s.ManagerIsVersion(">2.22.x") && s.ClientIsVersion(">2.22.x")) {
		s.T().Skip("Not part of compatibility tests. The wiretap command was introduced in 2.23")
	}
	s.Suite.SetupSuite()
	s.tplPath = filepath.Join("testdata", "k8s", "generic.goyaml")
	s.tpl = &itest.Generic{
		Name:       s.svc,
		TargetPort: "http",
		Registry:   "ghcr.io/telepresenceio",
		Image:      "echo-server:latest",
		ServicePorts: []itest.ServicePort{
			{
				Number:     80,
				Name:       "http",
				TargetPort: "http-cp",
			},
		},
		ContainerPorts: []itest.ContainerPort{
			{
				Number: 8080,
				Name:   "http-cp",
			},
		},
	}
	ctx := s.Context()
	s.ApplyTemplate(ctx, s.tplPath, s.tpl)
	s.NoError(s.RolloutStatusWait(ctx, "deploy/"+s.svc))
}

func (s *wiretapSuite) TearDownSuite() {
	s.DeleteTemplate(s.Context(), s.tplPath, s.tpl)
}

func (s *wiretapSuite) startWiretapHandler(ctx context.Context, name, addr string, echoTo *chan string) (int, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp", addr)
	rq := s.Require()
	rq.NoError(err, "failed to listen on localhost")
	port := l.Addr().(*net.TCPAddr).Port
	sc := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&s.hitCount, 1)
			if *echoTo != nil {
				*echoTo <- fmt.Sprintf("Request served by %s on port %d\n", name, port)
			}
		}),
	}
	go func() {
		_ = sc.Serve(l)
	}()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		err = sc.Shutdown(ctx)
		if err != nil {
			clog.Errorf(ctx, "http server on %s exited with error: %v", addr, err)
		} else {
			clog.Errorf(ctx, "http server on %s exited", addr)
		}
	}()
	return port, cancel
}

func (s *wiretapSuite) Test_MultipleTapsOnOnePort() { //nolint:gocognit
	ctx := s.Context()
	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	var out1 chan string
	localPort1, tap1HandlerCancel := s.startWiretapHandler(ctx, s.svc+"-wt1", ":0", &out1)
	defer tap1HandlerCancel()

	var out2 chan string
	localPort2, tap2HandlerCancel := s.startWiretapHandler(ctx, s.svc+"-wt2", ":0", &out2)
	defer tap2HandlerCancel()

	localPort3, ihCancel := itest.StartLocalHttpEchoServer(ctx, s.svc)
	defer ihCancel()

	s.Run("Place wiretap 1", func() {
		so := itest.TelepresenceOk(ctx, "wiretap", "--workload", s.svc, "--mount=false", "--port", fmt.Sprintf("%d:80", localPort1), "wt1")
		s.CapturePodLogs(ctx, s.svc, "traffic-agent", s.AppNamespace())
		s.Contains(so, "Using Deployment "+s.svc)
	})

	var podName string
	s.Run("Find pod with agent", func() {
		// Retrieve the name of the wiretapped pod
		ctx := s.Context()
		s.Eventually(func() bool {
			pods := itest.RunningPodsWithAgents(ctx, s.svc, s.AppNamespace())
			clog.Infof(ctx, "%s pods with agents: %v", s.svc, pods)
			if len(pods) != 1 {
				return false
			}
			podName = pods[0]
			return true
		}, 30*time.Second, 3*time.Second)
	})

	s.Run("Place wiretap 2", func() {
		so := itest.TelepresenceOk(s.Context(), "wiretap", "--workload", s.svc, "--mount=false", "--port", fmt.Sprintf("%d:80", localPort2), "wt2")
		s.Contains(so, "Using Deployment "+s.svc)
	})

	s.Run("Verify taps", func() {
		var ok1, ok2 bool
		out1 = make(chan string, 5)
		defer close(out1)
		out2 = make(chan string, 5)
		defer close(out2)
		s.Eventually(func() bool {
			so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", s.svc)
			// Output must yield the standard response from the cluster's service
			if err != nil {
				clog.Errorf(ctx, "curl: %s", err)
				return false
			}

			clog.Infof(ctx, "curl output: %s", so)
			if !strings.Contains(so, `Request served by `+podName) {
				return false
			}
			if !ok1 {
				select {
				case out := <-out1:
					ok1 = strings.Contains(out, fmt.Sprintf("Request served by %s-wt1 on port %d\n", s.svc, localPort1))
				default:
				}
			}
			if !ok2 {
				select {
				case out := <-out2:
					ok2 = strings.Contains(out, fmt.Sprintf("Request served by %s-wt2 on port %d\n", s.svc, localPort2))
				default:
				}
			}
			return ok1 && ok2
		}, 30*time.Second, 3*time.Second)

		so := itest.TelepresenceOk(ctx, "list", "--wiretaps", "--format", "json")
		var soj []connector.WorkloadInfo
		s.Require().NoError(json.Unmarshal([]byte(so), &soj))
		if s.Len(soj, 1) {
			iis := soj[0].InterceptInfo
			if s.Len(iis, 2) {
				s.True(iis[0].Spec.Wiretap)
				s.True(iis[1].Spec.Wiretap)
			}
		}

		so = itest.TelepresenceOk(ctx, "list", "--intercepts", "--format", "json")
		soj = nil
		s.Require().NoError(json.Unmarshal([]byte(so), &soj))
		s.Len(soj, 0)
	})

	s.Run("Verify tap concurrency", func() {
		// Perform 100 http requests spread out over a one-second period.
		const requestCount = int32(100)
		out1 = nil
		out2 = nil
		atomic.StoreInt32(&s.hitCount, 0)
		wg := sync.WaitGroup{}
		wg.Add(int(requestCount))
		for i := 0; i < int(requestCount); i++ {
			go func() {
				defer wg.Done()
				time.Sleep(time.Duration(rand.Float64() * float64(time.Second)))
				_, err := itest.Output(ctx, "curl", "--silent", "--max-time", "30", s.svc)
				s.NoError(err)
			}()
		}
		wg.Wait()
		time.Sleep(time.Second)
		s.Require().Equal(requestCount*2, atomic.LoadInt32(&s.hitCount))
	})

	s.Run("Intercept wiretapped service", func() {
		out1 = make(chan string, 5)
		defer close(out1)
		out2 = make(chan string, 5)
		defer close(out2)
		so := itest.TelepresenceOk(ctx, "intercept", "--mount=false", "--port", fmt.Sprintf("%d:80", localPort3), s.svc)
		defer itest.TelepresenceOk(ctx, "detach", s.svc)

		s.Contains(so, "Using Deployment "+s.svc)
		itest.PingInterceptedEchoServer(ctx, s.svc, "80")

		// Both handlers should have produced output.
		var ok1, ok2 bool
		s.Eventually(func() bool {
			if !ok1 {
				select {
				case out := <-out1:
					ok1 = strings.Contains(out, fmt.Sprintf("Request served by %s-wt1 on port %d\n", s.svc, localPort1))
				default:
				}
			}
			if !ok2 {
				select {
				case out := <-out2:
					ok2 = strings.Contains(out, fmt.Sprintf("Request served by %s-wt2 on port %d\n", s.svc, localPort2))
				default:
				}
			}
			return ok1 && ok2
		}, 5*time.Second, 3*time.Second)

		so = itest.TelepresenceOk(ctx, "list", "--wiretaps", "--format", "json")
		var soj []connector.WorkloadInfo
		s.Require().NoError(json.Unmarshal([]byte(so), &soj))
		if s.Len(soj, 1) {
			iis := soj[0].InterceptInfo
			if s.Len(iis, 2) {
				s.True(iis[0].Spec.Wiretap)
				s.True(iis[1].Spec.Wiretap)
			}
		}

		so = itest.TelepresenceOk(ctx, "list", "--intercepts", "--format", "json")
		soj = nil
		s.Require().NoError(json.Unmarshal([]byte(so), &soj))
		if s.Len(soj, 1) {
			iis := soj[0].InterceptInfo
			if s.Len(iis, 1) {
				s.False(iis[0].Spec.Wiretap)
			}
		}
	})

	s.Run("Verify taps after intercept", func() {
		out1 = make(chan string, 5)
		defer close(out1)
		out2 = make(chan string, 5)
		defer close(out2)

		so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", s.svc)
		s.NoError(err)
		// Output must yield the standard response from the cluster's service
		s.Contains(so, `Request served by `+podName)

		var ok1, ok2 bool
		s.Eventually(func() bool {
			if !ok1 {
				select {
				case out := <-out1:
					ok1 = strings.Contains(out, fmt.Sprintf("Request served by %s-wt1 on port %d\n", s.svc, localPort1))
				default:
				}
			}
			if !ok2 {
				select {
				case out := <-out2:
					ok2 = strings.Contains(out, fmt.Sprintf("Request served by %s-wt2 on port %d\n", s.svc, localPort2))
				default:
				}
			}
			return ok1 && ok2
		}, 5*time.Second, 3*time.Second)
	})

	s.Run("Wiretaps gone", func() {
		out1 = make(chan string, 5)
		defer close(out1)
		out2 = make(chan string, 5)
		defer close(out2)

		itest.TelepresenceOk(ctx, "detach", "wt1")
		itest.TelepresenceOk(ctx, "detach", "wt2")

		so, err := itest.Output(ctx, "curl", "--silent", "--max-time", "2", s.svc)
		s.NoError(err)
		// Out must yield the standard response from the cluster's service
		s.Contains(so, `Request served by `+podName)

		// Handlers should not have produced output.
		select {
		case out := <-out1:
			s.Failf("unexpected output from wiretap handler: %q", out)
		case out := <-out2:
			s.Failf("unexpected output from wiretap handler: %q", out)
		case <-time.After(2 * time.Second):
		}
	})
}

// Test_HTTPFilteredWiretap verifies that "telepresence wiretap --http-header
// ..." taps only the traffic that matches the header filter, via the
// traffic-agent's HTTP listener and reverse-proxy transport: a request
// carrying the header keeps reaching the real application (a wiretap never
// steals traffic) and a copy of it arrives at the local tap, while a
// request without the header also keeps reaching the real application but
// is never copied to the tap. Mirrors the node-agent variant,
// Test_NodeAgentHTTPFilteredWiretap in node_agent_test.go.
func (s *wiretapSuite) Test_HTTPFilteredWiretap() {
	ctx := s.Context()
	rq := s.Require()

	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	origPods := itest.RunningPods(ctx, s.svc, s.AppNamespace())
	rq.Len(origPods, 1, "expected exactly one running %s pod before wiretapping", s.svc)

	var tapOut chan string
	tapPort, tapCancel := s.startWiretapHandler(ctx, s.svc+"-httpfilter", ":0", &tapOut)
	defer tapCancel()

	const headerName = "x-telepresence-test"
	const headerValue = "http-filter-wiretap"
	header := headerName + "=" + headerValue

	stdout := itest.TelepresenceOk(ctx, "wiretap",
		"--workload", s.svc,
		"--mount=false",
		"--port", fmt.Sprintf("%d:80", tapPort),
		"--http-header", header,
		"wt-filtered")
	s.Contains(stdout, "Using Deployment "+s.svc)
	s.CapturePodLogs(ctx, s.svc, "traffic-agent", s.AppNamespace())
	mustLeave := true
	defer func() {
		if mustLeave {
			itest.TelepresenceOk(ctx, "detach", "wt-filtered")
		}
	}()

	curlWithHeader := func() (string, error) {
		return itest.Output(ctx, "curl", "--silent", "--max-time", "2", "-H", headerName+": "+headerValue, s.svc)
	}
	curlNoHeader := func() (string, error) {
		return itest.Output(ctx, "curl", "--silent", "--max-time", "2", s.svc)
	}

	// A request carrying the filter header must still reach the real
	// application: a wiretap never steals traffic, it only copies it.
	rq.Eventually(func() bool {
		so, err := curlWithHeader()
		if err != nil {
			return false
		}
		return strings.Contains(so, "Request served by ")
	}, 30*time.Second, 3*time.Second, "application did not keep serving header-matching traffic through the wiretap pass-through")

	// The tap must receive a copy of the header-matching traffic. Since tap
	// delivery is asynchronous, issue a request in each iteration and then
	// check if a tap hit arrives.
	tapOut = make(chan string, 5)
	rq.Eventually(func() bool {
		_, _ = curlWithHeader() // curl output is ignored; we only care about tap delivery
		select {
		case <-tapOut:
			return true
		default:
			return false
		}
	}, 30*time.Second, 3*time.Second, "wiretap tap did not receive a copy of the header-matching traffic")

	// Drain any straggler hits left over from the positive phase above --
	// tap delivery is asynchronous, so a copy of the last matched request
	// could still be in flight -- before checking the negative case below.
	drainDeadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case <-tapOut:
		case <-drainDeadline:
			break drain
		}
	}

	// A request without the header must keep reaching the real application.
	rq.Eventually(func() bool {
		so, err := curlNoHeader()
		if err != nil {
			return false
		}
		return strings.Contains(so, "Request served by ")
	}, 30*time.Second, 3*time.Second, "application did not keep serving non-matching traffic through the wiretap pass-through")

	// ...but it must never reach the tap. Issue several non-matching
	// requests, then assert no tap hit shows up within a modest window.
	for i := 0; i < 5; i++ {
		_, _ = curlNoHeader() // curl output is ignored; we only care about tap delivery
	}
	select {
	case out := <-tapOut:
		s.Failf("wiretap tap received a copy of traffic that did not match the header filter", "output: %q", out)
	case <-time.After(2 * time.Second):
	}

	itest.TelepresenceOk(ctx, "detach", "wt-filtered")
	mustLeave = false
}

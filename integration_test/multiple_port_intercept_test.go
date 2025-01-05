package integration_test

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type multiportInterceptSuite struct {
	itest.Suite
	itest.TrafficManager
	servicePort   [4]int
	serviceCancel [4]context.CancelFunc
	workloads     [2]string
	ports         [2][2]string
}

func (s *multiportInterceptSuite) SuiteName() string {
	return "MultiPortIntercept"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &multiportInterceptSuite{
			Suite:          itest.Suite{Harness: h},
			TrafficManager: h,
			workloads:      [2]string{"echo-both", "echo-double-one-unnamed"},
			ports:          [2][2]string{{"one", "two"}, {"8080", "8081"}},
		}
	})
}

func (s *multiportInterceptSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	for i := 0; i < 4; i++ {
		s.servicePort[i], s.serviceCancel[i] = itest.StartLocalHttpEchoServer(ctx, fmt.Sprintf("%s-%d", "echo", i))
	}
	wg := sync.WaitGroup{}
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			dep := s.workloads[i]
			s.ApplyApp(ctx, dep, "deploy/"+dep)
		}()
	}
	s.TelepresenceConnect(ctx)
	wg.Wait()
}

func (s *multiportInterceptSuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuitOk(ctx)
	for i := 0; i < 2; i++ {
		s.DeleteApp(ctx, s.workloads[i])
	}
	for i := 0; i < 4; i++ {
		s.serviceCancel[i]()
	}
}

func (s *multiportInterceptSuite) Test_MultiPortIntercept() {
	ctx := s.Context()
	for i := 0; i < 2; i++ {
		itest.TelepresenceOk(ctx, "intercept", s.workloads[i],
			"--mount=false",
			"--port", fmt.Sprintf("%d:%s", s.servicePort[i*2], s.ports[i][0]),
			"--port", fmt.Sprintf("%d:%s", s.servicePort[i*2+1], s.ports[i][1]))
	}
	defer func() {
		for i := 0; i < 2; i++ {
			itest.TelepresenceOk(ctx, "leave", s.workloads[i])
		}
	}()

	rq := s.Require()
	rq.Eventually(func() bool {
		st := itest.TelepresenceStatusOk(ctx)
		ics := st.UserDaemon.Intercepts
		if len(ics) != 2 {
			return false
		}
		for i := 0; i < 2; i++ {
			ic := ics[i]
			if ic.Name != s.workloads[i] {
				return false
			}
		}
		return true
	}, 10*time.Second, time.Second)

	var edoPods []core.Pod
	rq.Eventually(func() bool {
		edoPods = itest.RunningPods(ctx, s.workloads[1], s.AppNamespace())
		return len(edoPods) == 1
	}, 30*time.Second, 3*time.Second)

	edoPodIP := edoPods[0].Status.PodIP
	ports := [4]string{"80", "80", "8080", "8081"}
	for i, svc := range [4]string{"echo-one", "echo-two", edoPodIP, edoPodIP} {
		itest.PingInterceptedEchoServer(ctx, fmt.Sprintf("%s/echo-%d", svc, i), ports[i])
	}
}

func (s *multiportInterceptSuite) Test_MultiPortLocalConflict() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "intercept", s.workloads[0],
		"--mount=false",
		"--port", fmt.Sprintf("%d:%s", s.servicePort[0], s.ports[0][0]),
		"--port", fmt.Sprintf("%d:%s", s.servicePort[1], s.ports[0][1]))
	defer itest.TelepresenceOk(ctx, "leave", s.workloads[0])

	_, _, err := itest.Telepresence(ctx, "intercept", s.workloads[1],
		"--mount=false",
		"--port", fmt.Sprintf("%d:%s", s.servicePort[2], s.ports[1][0]),
		"--port", fmt.Sprintf("%d:%s", s.servicePort[1], s.ports[1][1]))
	s.Require().Error(err)
	s.Contains(err.Error(), fmt.Sprintf("%d is already in use by intercept %s", s.servicePort[1], s.workloads[0]))
}

func (s *multiportInterceptSuite) Test_MultiPortRemoteConflict() {
	ctx := s.Context()
	itest.TelepresenceOk(ctx, "intercept", s.workloads[0],
		"--mount=false",
		"--port", fmt.Sprintf("%d:%s", s.servicePort[0], s.ports[0][0]),
		"--port", fmt.Sprintf("%d:%s", s.servicePort[1], s.ports[0][1]))
	defer itest.TelepresenceOk(ctx, "leave", s.workloads[0])

	_, _, err := itest.Telepresence(ctx, "intercept", "--workload", s.workloads[0], s.workloads[0]+"-again",
		"--mount=false",
		"--port", strconv.Itoa(s.servicePort[1]), "--service", "echo-two")
	s.Require().Error(err)
	s.Regexp(fmt.Sprintf(`container port 8081 is already intercepted by \S+ intercept %s`, s.workloads[0]), err.Error())
}

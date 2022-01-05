package integration_test

import (
	"sync"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) Test_ToPodPortForwarding() {
	const svc = "echo-w-sidecars"
	ctx := s.Context()
	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer func() {
		_ = s.Kubectl(ctx, "delete", "svc,deploy", svc)
	}()

	require := s.Require()
	stdout := itest.TelepresenceOk(ctx, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", svc, "--port", "8080", "--to-pod", "8081", "--to-pod", "8082")
	defer itest.TelepresenceOk(ctx, "leave", svc+"-"+s.AppNamespace())
	require.Contains(stdout, "Using Deployment "+svc)
	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace(), "--intercepts")
	require.Contains(stdout, svc+": intercepted")

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", "localhost:8081") == nil
		}, 15*time.Second, 2*time.Second, "Forwarded port is not reachable as localhost:8081")
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", "localhost:8082") == nil
		}, 15*time.Second, 2*time.Second, "Forwarded port is not reachable as localhost:8082")
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", "localhost:8083") != nil
		}, 15*time.Second, 2*time.Second, "Non-forwarded port is reachable")
	}()
	wg.Wait()
}

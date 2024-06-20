package integration_test

import (
	"fmt"
	"net"
	"regexp"
	"sync"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

func (s *connectedSuite) Test_ToPodPortForwarding() {
	const svc = "echo-w-sidecars"
	ctx := s.Context()
	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	require := s.Require()
	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", svc, "--port", "8080", "--to-pod", "8081", "--to-pod", "8082")
	defer itest.TelepresenceOk(ctx, "leave", svc)
	require.Contains(stdout, "Using Deployment "+svc)
	s.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		return err == nil && regexp.MustCompile(svc+`\s*: intercepted`).MatchString(stdout)
	}, 10*time.Second, time.Second)

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", "localhost:8081") == nil
		}, 30*time.Second, 2*time.Second, "Forwarded port is not reachable as localhost:8081")
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", "localhost:8082") == nil
		}, 30*time.Second, 2*time.Second, "Forwarded port is not reachable as localhost:8082")
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", "localhost:8083") != nil
		}, 30*time.Second, 2*time.Second, "Non-forwarded port is reachable")
	}()
	wg.Wait()
}

func (s *connectedSuite) Test_ToPodUDPPortForwarding() {
	const svc = "echo-extra-udp"
	ctx := s.Context()
	s.ApplyApp(ctx, svc, "deploy/"+svc)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	require := s.Require()
	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", svc, "--port", "9080", "--to-pod", "8080/UDP")
	defer itest.TelepresenceOk(ctx, "leave", svc)
	require.Contains(stdout, "Using Deployment "+svc)
	stdout = itest.TelepresenceOk(ctx, "list", "--intercepts")
	require.Contains(stdout, svc+": intercepted")
	itest.TelepresenceOk(ctx, "loglevel", "trace")
	defer itest.TelepresenceOk(ctx, "loglevel", "debug")
	s.CapturePodLogs(ctx, svc, "traffic-agent", s.AppNamespace())

	conn, err := net.Dial("udp", "localhost:8080")
	require.NoError(err)
	defer conn.Close()

	pingPong := func(msg string) {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		bm := []byte(msg)
		n, err := conn.Write(bm)
		require.NoError(err)
		require.Equal(len(bm), n)
		buf := [0x100]byte{}
		n, err = conn.Read(buf[:])
		require.NoError(err)
		require.Equal(fmt.Sprintf("Reply from UDP-echo: %s", msg), string(buf[0:n]))
	}

	pingPong("12345678")
	pingPong("a slightly longer message")
}

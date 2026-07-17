package integration_test

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
)

func (s *connectedSuite) Test_ToPodPortForwarding() {
	const svc = "echo-w-sidecars"
	ctx := s.Context()
	require := s.Require()

	// The local side of a to-pod forward uses the pod's own port number, so
	// the sidecar ports must be free on the workstation. Pick free ports and
	// render them into the workload, so the test doesn't collide with
	// unrelated local listeners. The third sidecar's port is not forwarded;
	// it proves that only the requested ports are.
	fps, err := ioutil.FreePortsTCP(3)
	require.NoError(err)
	ports := struct {
		PortOne   uint16
		PortTwo   uint16
		PortThree uint16
	}{fps[0].Port(), fps[1].Port(), fps[2].Port()}
	portOne := strconv.Itoa(int(ports.PortOne))
	portTwo := strconv.Itoa(int(ports.PortTwo))
	portThree := strconv.Itoa(int(ports.PortThree))

	tplPath := filepath.Join("testdata", "k8s", "echo-w-sidecars.goyaml")
	s.ApplyTemplate(ctx, tplPath, &ports)
	defer s.DeleteTemplate(ctx, tplPath, &ports)
	require.NoError(s.RolloutStatusWait(ctx, "deploy/"+svc))

	stdout := itest.TelepresenceOk(ctx, "intercept", "--mount", "false", svc, "--port", "8080", "--to-pod", portOne, "--to-pod", portTwo)
	defer itest.TelepresenceOk(ctx, "detach", svc)
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
			return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", "localhost:"+portOne) == nil
		}, 30*time.Second, 2*time.Second, "Forwarded port is not reachable as localhost:"+portOne)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", "localhost:"+portTwo) == nil
		}, 30*time.Second, 2*time.Second, "Forwarded port is not reachable as localhost:"+portTwo)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Eventually(func() bool {
			return itest.Run(ctx, "curl", "--silent", "--max-time", "0.5", "localhost:"+portThree) != nil
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
	defer itest.TelepresenceOk(ctx, "detach", svc)
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

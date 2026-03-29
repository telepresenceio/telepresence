package integration_test

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"time"

	"github.com/docker/docker/api/types/network"
	dockerClient "github.com/docker/docker/client"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

type composeSuite struct {
	itest.Suite
	itest.TrafficManager
	ctx context.Context
}

func (s *composeSuite) SuiteName() string {
	return "Compose"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &composeSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (s *composeSuite) SetupSuite() {
	if s.IsCI() && !(goRuntime.GOOS == "linux" && goRuntime.GOARCH == "amd64") {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
		return
	}
	s.Suite.SetupSuite()
	s.ctx = itest.WithConfig(s.HarnessContext(), func(cfg client.Config) {
		cfg.Intercept().UseFtp = false
	})
}

func (s *composeSuite) TearDownTest() {
	itest.TelepresenceQuitOk(s.Context())
}

func (s *composeSuite) Context() context.Context {
	return itest.WithT(s.ctx, s.T())
}

func (s *composeSuite) Test_ComposeDNS() {
	ctx := s.Context()
	require := s.Require()

	const svc = "echo-easy"
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	const svc2 = "echo-other"
	s.ApplyEchoService(ctx, svc2, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc2)

	// Write a docker-compose.yml that replaces echo-other and sleeps.
	composeDir := itest.TempDir(ctx)
	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	ns := s.AppNamespace()
	composeContent := strings.Join([]string{
		"x-tele:",
		"  connections:",
		"    - namespace: " + ns,
		"      manager-namespace: " + s.ManagerNamespace(),
		"services:",
		"  " + svc2 + ":",
		"    x-tele:",
		"      type: replace",
		"    image: busybox",
		"    command: sleep infinity",
	}, "\n")
	require.NoError(os.WriteFile(composeFile, []byte(composeContent), 0o644))

	_, _, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "up", "-d")
	require.NoError(err, "compose up failed")
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "down")
	}()

	// Verify that cluster DNS resolves inside the compose container.
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "exec", svc2, "nslookup", svc)
		if err == nil && strings.Contains(so, "Address:") {
			return true
		}
		clog.Info(ctx, "nslookup", "sdtout", so, "stderr", se, "err", err)
		return false
	}, 30*time.Second, 3*time.Second, "nslookup of %s in compose container should succeed", svc)
}

func (s *composeSuite) Test_ComposeConnect() {
	ctx := s.Context()
	require := s.Require()

	const svc = "echo-easy"
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	composeDir := itest.TempDir(ctx)
	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	ns := s.AppNamespace()
	composeContent := strings.Join([]string{
		"x-tele:",
		"  connections:",
		"    - namespace: " + ns,
		"      manager-namespace: " + s.ManagerNamespace(),
		"services:",
		"  tester:",
		"    x-tele:",
		"      type: connect",
		"    image: busybox",
		"    command: sleep infinity",
	}, "\n")
	require.NoError(os.WriteFile(composeFile, []byte(composeContent), 0o644))

	_, _, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "up", "-d")
	require.NoError(err, "compose up failed")
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "down")
	}()

	// Verify HTTP access to a cluster service from the connect container using its single-label name.
	// The tel2-search DNS search domain causes the resolver to first try "echo-easy.tel2-search",
	// which Docker's embedded DNS forwards to the Telepresence daemon DNS. The daemon strips the
	// tel2-search suffix and resolves the bare name against the cluster.
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "exec", "tester", "wget", "-qO-", "http://"+svc)
		if err == nil && len(so) > 0 {
			return true
		}
		clog.Info(ctx, "wget", "stdout", so, "stderr", se, "err", err)
		return false
	}, 30*time.Second, 3*time.Second, "wget of %s from connect container should succeed", svc)
}

func (s *composeSuite) Test_ComposeProxy() {
	ctx := s.Context()
	require := s.Require()

	const svc = "echo-easy"
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	composeDir := itest.TempDir(ctx)
	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	ns := s.AppNamespace()
	composeContent := strings.Join([]string{
		"x-tele:",
		"  connections:",
		"    - namespace: " + ns,
		"      manager-namespace: " + s.ManagerNamespace(),
		"services:",
		"  " + svc + ":",
		"    x-tele:",
		"      type: proxy",
		"  tester:",
		"    x-tele:",
		"      type: connect",
		"    image: busybox",
		"    command: sleep infinity",
	}, "\n")
	require.NoError(os.WriteFile(composeFile, []byte(composeContent), 0o644))

	_, _, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "up", "-d")
	require.NoError(err, "compose up failed")
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "down")
	}()

	// Verify that HTTP requests are routed through the proxy to the cluster service.
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "exec", "tester", "wget", "-qO-", "http://"+svc)
		if err == nil && len(so) > 0 {
			return true
		}
		clog.Info(ctx, "wget via proxy", "stdout", so, "stderr", se, "err", err)
		return false
	}, 30*time.Second, 3*time.Second, "wget of %s through proxy should succeed", svc)
}

func (s *composeSuite) Test_ComposeIngest() {
	ctx := s.Context()
	require := s.Require()

	const ingestSvc = "ingest-app"
	itest.ApplyAppTemplate(ctx, s.AppNamespace(), &itest.AppData{
		AppName: ingestSvc,
		Image:   "ghcr.io/telepresenceio/echo-server:0.3.1",
		Ports: []itest.AppPort{
			{ServicePortNumber: 80, TargetPortNumber: 8080},
		},
		Env: map[string]string{
			"INGEST_TEST_VAR": "hello-from-cluster",
		},
	})
	defer s.DeleteSvcAndWorkload(ctx, "deploy", ingestSvc)

	composeDir := itest.TempDir(ctx)
	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	ns := s.AppNamespace()
	composeContent := strings.Join([]string{
		"x-tele:",
		"  connections:",
		"    - namespace: " + ns,
		"      manager-namespace: " + s.ManagerNamespace(),
		"services:",
		"  " + ingestSvc + ":",
		"    x-tele:",
		"      type: ingest",
		"    image: busybox",
		"    command: sleep infinity",
	}, "\n")
	require.NoError(os.WriteFile(composeFile, []byte(composeContent), 0o644))

	_, _, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "up", "-d")
	require.NoError(err, "compose up failed")
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "down")
	}()

	// Verify that the ingest container inherits the environment variable from the cluster workload.
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "exec", ingestSvc, "env")
		if err == nil && strings.Contains(so, "INGEST_TEST_VAR=hello-from-cluster") {
			return true
		}
		clog.Info(ctx, "env check", "stdout", so, "stderr", se, "err", err)
		return false
	}, 60*time.Second, 5*time.Second, "ingest container should inherit INGEST_TEST_VAR from cluster workload")
}

func (s *composeSuite) Test_ComposeIntercept() {
	ctx := s.Context()
	require := s.Require()

	const svc = "echo-easy"
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	composeDir := itest.TempDir(ctx)
	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	ns := s.AppNamespace()
	composeContent := strings.Join([]string{
		"x-tele:",
		"  connections:",
		"    - namespace: " + ns,
		"      manager-namespace: " + s.ManagerNamespace(),
		"services:",
		"  " + svc + ":",
		"    x-tele:",
		"      type: intercept",
		"      ports:",
		`        - "80:80"`,
		"    image: hashicorp/http-echo",
		`    command: ["-text=hello-from-compose", "-listen=:80"]`,
		"  tester:",
		"    x-tele:",
		"      type: connect",
		"    image: busybox",
		"    command: sleep infinity",
	}, "\n")
	require.NoError(os.WriteFile(composeFile, []byte(composeContent), 0o644))

	_, _, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "up", "-d")
	require.NoError(err, "compose up failed")
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "down")
	}()

	// Verify that cluster traffic is intercepted and served by the local compose container.
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "exec", "tester", "wget", "-qO-", "http://"+svc)
		if err == nil && strings.Contains(so, "hello-from-compose") {
			return true
		}
		clog.Info(ctx, "wget intercept", "stdout", so, "stderr", se, "err", err)
		return false
	}, 60*time.Second, 5*time.Second, "intercept should redirect cluster traffic to the compose container")
}

func (s *composeSuite) Test_ComposeReplace() {
	ctx := s.Context()
	require := s.Require()

	const svc = "echo-easy"
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	composeDir := itest.TempDir(ctx)
	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	ns := s.AppNamespace()
	composeContent := strings.Join([]string{
		"x-tele:",
		"  connections:",
		"    - namespace: " + ns,
		"      manager-namespace: " + s.ManagerNamespace(),
		"services:",
		"  " + svc + ":",
		"    x-tele:",
		"      type: replace",
		"      ports:",
		`        - "80:8080"`, // local port 80 -> cluster container port 8080 (echo-server listens on 8080)
		"    image: hashicorp/http-echo",
		`    command: ["-text=hello-from-compose", "-listen=:80"]`,
		"  tester:",
		"    x-tele:",
		"      type: connect",
		"    image: busybox",
		"    command: sleep infinity",
	}, "\n")
	require.NoError(os.WriteFile(composeFile, []byte(composeContent), 0o644))

	_, _, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "up", "-d")
	require.NoError(err, "compose up failed")
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "down")
	}()

	// Verify that the local compose container serves traffic in place of the cluster pod.
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "exec", "tester", "wget", "-qO-", "http://"+svc)
		if err == nil && strings.Contains(so, "hello-from-compose") {
			return true
		}
		clog.Info(ctx, "wget replace", "stdout", so, "stderr", se, "err", err)
		return false
	}, 60*time.Second, 5*time.Second, "replace should serve traffic from the compose container instead of the cluster pod")
}

func (s *composeSuite) Test_ComposeWiretap() {
	ctx := s.Context()
	require := s.Require()

	const svc = "echo-easy"
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	composeDir := itest.TempDir(ctx)
	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	ns := s.AppNamespace()
	composeContent := strings.Join([]string{
		"x-tele:",
		"  connections:",
		"    - namespace: " + ns,
		"      manager-namespace: " + s.ManagerNamespace(),
		"services:",
		"  " + svc + ":",
		"    x-tele:",
		"      type: wiretap",
		"      ports:",
		`        - "80:80"`,
		"    image: hashicorp/http-echo",
		`    command: ["-text=hello-from-compose", "-listen=:80"]`,
		"  tester:",
		"    x-tele:",
		"      type: connect",
		"    image: busybox",
		"    command: sleep infinity",
	}, "\n")
	require.NoError(os.WriteFile(composeFile, []byte(composeContent), 0o644))

	_, _, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "up", "-d")
	require.NoError(err, "compose up failed")
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "down")
	}()

	// Use the namespace-qualified name to reach the cluster service rather than the local compose
	// container. Docker's embedded DNS resolves bare "echo-easy" to the local wiretap-receiver
	// container, which would bypass the cluster and never trigger the traffic-agent. A
	// namespace-qualified name is not known to Docker's embedded DNS, so it is forwarded to the
	// Telepresence daemon DNS which routes it to the cluster.
	clusterSvcName := svc + "." + ns

	// Verify that the cluster service still serves the original traffic (not the local container).
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "exec", "tester", "wget", "-qO-", "http://"+clusterSvcName)
		if err == nil && len(so) > 0 && !strings.Contains(so, "hello-from-compose") {
			return true
		}
		clog.Info(ctx, "wget wiretap cluster check", "stdout", so, "stderr", se, "err", err)
		return false
	}, 60*time.Second, 5*time.Second, "cluster service should still serve original traffic during wiretap")

	// Make several requests via the namespace-qualified name so the traffic-agent wiretap fires
	// and sends copies to the local container.
	for range 3 {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "exec", "tester", "wget", "-qO-", "http://"+clusterSvcName)
	}

	// Verify that the wiretap container received copies of the traffic via its logs.
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "logs", svc)
		if err == nil && strings.Contains(so, "GET") {
			return true
		}
		clog.Info(ctx, "compose logs wiretap", "stdout", so, "stderr", se, "err", err)
		return false
	}, 30*time.Second, 3*time.Second, "wiretap container should receive copies of traffic")
}

func (s *composeSuite) Test_ComposeDefaultNetworkNoSubnetConflict() {
	ctx := s.Context()
	rq := s.Require()

	const svc = "echo-easy"
	s.ApplyEchoService(ctx, svc, 80)
	defer s.DeleteSvcAndWorkload(ctx, "deploy", svc)

	// Use multiple named connections to the same namespace. Each connection creates a separate
	// daemon container with its own teleroute network, consuming Docker network address space.
	// Combined with existing Docker networks (bridge, kind, etc.), this pushes Docker's sequential
	// IPAM allocation toward the cluster's service CIDR (e.g. 172.20.0.0/16). Without the fix,
	// the compose default network would get a conflicting subnet.
	const projectName = "tp-subnet-test"
	composeDir := itest.TempDir(ctx)
	composeFile := filepath.Join(composeDir, "docker-compose.yml")
	ns := s.AppNamespace()
	mgrNs := s.ManagerNamespace()
	composeContent := strings.Join([]string{
		"x-tele:",
		"  connections:",
		"    - name: conn-1",
		"      namespace: " + ns,
		"      manager-namespace: " + mgrNs,
		"    - name: conn-2",
		"      namespace: " + ns,
		"      manager-namespace: " + mgrNs,
		"    - name: conn-3",
		"      namespace: " + ns,
		"      manager-namespace: " + mgrNs,
		"services:",
		"  tester-1:",
		"    x-tele:",
		"      type: connect",
		"      connection: conn-1",
		"    image: busybox",
		"    command: sleep infinity",
		"  tester-2:",
		"    x-tele:",
		"      type: connect",
		"      connection: conn-2",
		"    image: busybox",
		"    command: sleep infinity",
		"  tester-3:",
		"    x-tele:",
		"      type: connect",
		"      connection: conn-3",
		"    image: busybox",
		"    command: sleep infinity",
	}, "\n")
	rq.NoError(os.WriteFile(composeFile, []byte(composeContent), 0o644))

	_, _, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "--project-name", projectName, "up", "-d")
	rq.NoError(err, "compose up failed")
	defer func() {
		_, _, _ = itest.Telepresence(ctx, "compose", "-f", composeFile, "--project-name", projectName, "down")
	}()

	// Collect cluster CIDRs from the first connection's status.
	status := itest.TelepresenceStatusOk(ctx, "--use", "conn-1")
	rq.NotNil(status.RootDaemon)
	clusterSubnets := status.RootDaemon.Subnets
	alsoProxy := status.RootDaemon.AlsoProxy
	allClusterCIDRs := append(append([]netip.Prefix{}, clusterSubnets...), alsoProxy...)
	rq.NotEmpty(allClusterCIDRs, "expected at least one cluster subnet in status")

	// Inspect the compose default network and verify its subnet doesn't overlap with any cluster CIDR.
	cli, err := dockerClient.NewClientWithOpts(dockerClient.FromEnv, dockerClient.WithAPIVersionNegotiation())
	rq.NoError(err)
	defer cli.Close()

	defaultNetworkName := projectName + "_default"
	ni, err := cli.NetworkInspect(ctx, defaultNetworkName, network.InspectOptions{})
	rq.NoError(err, "failed to inspect network %s", defaultNetworkName)
	rq.NotEmpty(ni.IPAM.Config, "expected at least one IPAM config on default network %s", defaultNetworkName)

	for _, cfg := range ni.IPAM.Config {
		subnet, err := netip.ParsePrefix(cfg.Subnet)
		if err != nil {
			continue // skip non-IPv4 or unparseable entries
		}
		for _, clusterCIDR := range allClusterCIDRs {
			s.Falsef(subnet.Overlaps(clusterCIDR),
				"compose default network %s subnet %s overlaps with cluster CIDR %s", defaultNetworkName, subnet, clusterCIDR)
		}
	}

	// Verify that cluster connectivity works from one of the connect containers.
	s.Assert().EventuallyContext(ctx, func() bool {
		so, se, err := itest.Telepresence(ctx, "compose", "-f", composeFile, "--project-name", projectName, "exec", "tester-1", "wget", "-qO-", "http://"+svc)
		if err == nil && len(so) > 0 {
			return true
		}
		clog.Info(ctx, "wget", "stdout", so, "stderr", se, "err", err)
		return false
	}, 30*time.Second, 3*time.Second, "wget of %s from connect container should succeed", svc)
}

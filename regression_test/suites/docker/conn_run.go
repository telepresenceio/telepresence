package docker

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// connRunName is the named docker connection every test in this suite
// shares: established once (memoized by the fixture engine) and left for
// the run's own end-of-run teardown, exactly like the shared default
// connection every other area relies on -- never explicitly disconnected
// mid-suite, since a later test's identical rt.ConnNamed/rt.ConnDocker
// s.Connect call would otherwise be handed back a Conn whose session was
// already quit.
const connRunName = "rtest-connrun"

// connRunPollTimeout/connRunPollInterval bound every docker-CLI poll in this
// file: a handed-off container's network membership settles asynchronously
// after `docker start`.
const (
	connRunPollTimeout  = 30 * time.Second
	connRunPollInterval = time.Second
)

// DockerConnRun exercises the containerized-daemon side of --docker-run:
// docker_run_test.go's Test_DockerRunCommand, Test_DockerRunExternalDNS,
// and Test_DockerRun_VolumePresent, all originally on dockerDaemonSuite's
// `--docker` connection. Supersedes the dockerDaemonSuite docker-run tests.
type DockerConnRun struct {
	rt.Suite
}

func init() {
	rt.Register(&DockerConnRun{},
		rt.InArea("docker"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// ensureConn provisions this suite's shared named docker connection on
// first use; every test in this file addresses it by name (--use
// connRunName) rather than through the returned *rt.Conn.
func (s *DockerConnRun) ensureConn() {
	s.Connect(rt.ConnNamed(connRunName), rt.ConnDocker())
}

// dockerInspectNetworks decodes `docker inspect --format
// {{json .NetworkSettings.Networks}} <container>`: the set of docker
// networks container is a member of, keyed by network name.
func dockerInspectNetworks(ctx context.Context, container string) (map[string]struct{ IPAddress string }, error) {
	raw, err := dockerOutput(ctx, "inspect", "--format", "{{json .NetworkSettings.Networks}}", container)
	if err != nil {
		return nil, err
	}
	var networks map[string]struct{ IPAddress string }
	if err := json.Unmarshal([]byte(raw), &networks); err != nil {
		return nil, fmt.Errorf("decoding %s's networks: %w", container, err)
	}
	return networks, nil
}

// dockerInspectDNS decodes `docker inspect --format {{json .HostConfig.Dns}}
// <container>`: the container's own --dns docker-create flags.
func dockerInspectDNS(ctx context.Context, container string) ([]string, error) {
	raw, err := dockerOutput(ctx, "inspect", "--format", "{{json .HostConfig.Dns}}", container)
	if err != nil {
		return nil, err
	}
	var dns []string
	if err := json.Unmarshal([]byte(raw), &dns); err != nil {
		return nil, fmt.Errorf("decoding %s's DNS config: %w", container, err)
	}
	return dns, nil
}

// dockerNetworkIP returns container's own IP address within network, via
// `docker inspect --format {{(index .NetworkSettings.Networks "<network>").IPAddress}}`.
func dockerNetworkIP(ctx context.Context, container, network string) (string, error) {
	format := fmt.Sprintf(`{{(index .NetworkSettings.Networks %q).IPAddress}}`, network)
	return dockerOutput(ctx, "inspect", "--format", format, container)
}

// Test_JoinsDaemonNetwork proves a bare `telepresence docker-run` container
// joins the daemon's own teleroute network and inherits its DNS. The
// teleroute network and the daemon container share one name
// (pkg/client/docker/teleroute/network.go's CreateNetwork: `cn :=
// info.Name`; pkg/client/docker/daemon.go's DaemonOptions: `--name
// daemonID.ContainerName()`), and a named connection's daemon identifier is
// exactly its --name (pkg/client/cli/daemon/identifier.go's NewIdentifier),
// so connRunName is both the network to look for and the container whose
// own IP is the expected DNS server (pkg/client/docker/daemon.go's
// GetDaemonContainerNetworkInfo). Supersedes docker_run_test.go's
// Test_DockerRunCommand, replacing its `ip r` / "dev tpd-0" text scrape
// with `docker inspect`.
func (s *DockerConnRun) Test_JoinsDaemonNetwork() {
	t := s.T()
	ctx := s.Ctx()
	s.ensureConn()

	const handoff = "rtest-connrun-network-handler"
	_ = exec.Command("docker", "rm", "-f", handoff).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", handoff).Run() })

	p, err := s.CLI().Start(ctx, "docker-run", "--use", connRunName, "--rm", "-i", "--name", handoff,
		"busybox", "sleep", "30")
	s.Require().NoError(err, "starting docker-run")
	defer func() {
		_ = p.Signal(os.Interrupt)
		waitProcExit(t, p, connRunPollTimeout)
	}()

	s.Eventually(func() bool {
		networks, err := dockerInspectNetworks(ctx, handoff)
		if err != nil {
			return false
		}
		_, ok := networks[connRunName]
		return ok
	}, connRunPollTimeout, connRunPollInterval,
		"handed-off container should be a member of the daemon network %s", connRunName)

	daemonIP, err := dockerNetworkIP(ctx, connRunName, connRunName)
	s.Require().NoError(err, "inspecting daemon container %s's own IP in its network", connRunName)
	s.NotEmpty(daemonIP, "daemon container %s should have an IP in its own teleroute network", connRunName)

	dns, err := dockerInspectDNS(ctx, handoff)
	s.Require().NoError(err, "inspecting %s's DNS config", handoff)
	s.Contains(dns, daemonIP, "handed-off container's DNS should be the daemon container's own IP")
}

// Test_ExternalDNSResolves proves a bare `telepresence docker-run` container
// resolves external names through the daemon's own DNS: docker_run_test.go's
// Test_DockerRunExternalDNS.
func (s *DockerConnRun) Test_ExternalDNSResolves() {
	ctx := s.Ctx()
	s.ensureConn()

	stdout, stderr, err := s.CLI().Run(ctx, "docker-run", "--use", connRunName, "--rm", "-i",
		"busybox", "nslookup", "google.com")
	s.Require().NoError(err, "docker-run nslookup: %s", stderr)
	s.Contains(stdout, "Address: ", "nslookup of an external name should resolve through the daemon's DNS")
}

// serviceAccountMountPath is the path Kubernetes auto-mounts a pod's
// serviceaccount token at, present on any pod that hasn't disabled
// automountServiceAccountToken -- which workloads.Echo doesn't (mirrors
// mounts/helpers.go's tokenRelPath, absolute form).
const serviceAccountMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"

// connRunVolumeLocalPort is the local half of this test's --docker-run
// intercept's --port mapping; the remote half names workloads.Echo's own
// "http" service port.
const connRunVolumeLocalPort = 9072

// Test_ServiceAccountMountPresent proves an `intercept --docker-run`
// attachment on a docker connection carries the remote telemount volume
// into the handed-off container at the same absolute path
// (pkg/client/cli/docker/runner.go's adjustMounts: `--mount
// type=bind,src=...,dst=<path>`), listing the serviceaccount secret's
// "namespace" entry. Supersedes docker_run_test.go's
// Test_DockerRun_VolumePresent, replacing its bespoke
// testdata/k8s/hello-w-volumes.goyaml secret mount with the serviceaccount
// token every pod already carries.
func (s *DockerConnRun) Test_ServiceAccountMountPresent() {
	ctx := s.Ctx()
	s.ensureConn()
	wl := s.Workload(workloads.Echo("connrun-volume"))

	stdout, stderr, err := s.CLI().Run(ctx,
		"intercept", wl.Name, "--namespace", wl.Namespace, "--use", connRunName,
		"--port", fmt.Sprintf("%d:http", connRunVolumeLocalPort),
		"--docker-run", "--",
		"--rm", "-i", "busybox", "ls", serviceAccountMountPath)
	s.Require().NoError(err, "intercept --docker-run: %s", stderr)
	s.Contains(stdout, "namespace", "serviceaccount mount should list its namespace entry")
}

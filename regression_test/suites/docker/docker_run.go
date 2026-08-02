package docker

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	// dockerRunImage is the handler image: the published echo-server, whose
	// container's own hostname (not the cluster pod's) shows up in its
	// "Request served by <hostname>" response line once intercepted.
	dockerRunImage = "ghcr.io/telepresenceio/echo-server:0.3.1"

	// dockerRunLocalPort/dockerRunContainerPort are the intercept's --port
	// <local>:<container> pair (docker_run_test.go's exact shape): the
	// handler container's port 8080 (the workload's own container port,
	// workloads.Echo's default) gets published to localPort on the host,
	// and traffic to the workload's service is redirected there.
	dockerRunLocalPort     = 9070
	dockerRunContainerPort = 8080

	// dockerRunHostname/dockerRunContainerName identify the handler
	// container: --hostname makes echo-server's response line
	// deterministic and distinct from the cluster pod's own hostname
	// (Kubernetes sets a pod's hostname to its pod name).
	dockerRunHostname      = "rtest-dockerrun-handler"
	dockerRunContainerName = "rtest-dockerrun-handler"

	dockerRunAttachTimeout = 30 * time.Second
	dockerRunRouteTimeout  = 30 * time.Second
	dockerRunExitTimeout   = 15 * time.Second
)

// DockerRun proves `intercept --docker-run` routes cluster traffic to the
// handler container it starts, and that detaching hands the workload back
// to the cluster: docker_run_test.go's Test_DockerRun_HostDaemon
// essentials, against the published echo-server image instead of a locally
// built one.
type DockerRun struct {
	rt.Suite
}

func init() {
	rt.Register(&DockerRun{},
		rt.InArea("docker"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// servedByHostname is a check.EventuallyHTTP predicate matching a 200
// response whose body carries echo-server's "Request served by <hostname>"
// line for hostname.
func servedByHostname(hostname string) func(status int, body string) bool {
	marker := "Request served by " + hostname
	return func(status int, body string) bool {
		return status == http.StatusOK && strings.Contains(body, marker)
	}
}

// notServedByHostname is servedByHostname's negation, for the post-detach
// check that traffic reverts to the cluster pod (whose own hostname is its
// pod name, never dockerRunHostname).
func notServedByHostname(hostname string) func(status int, body string) bool {
	marker := "Request served by " + hostname
	return func(status int, body string) bool {
		return status == http.StatusOK && !strings.Contains(body, marker)
	}
}

// Test_HandlerServesIntercept starts `intercept --docker-run` against the
// echo-server image with its port published to the intercept (--port
// <local>:<container>, docker_run_test.go's exact shape), waits for it to
// appear in `list`, and asserts the service URL is served by the handler
// container -- distinguished from the cluster pod by its --hostname, since
// echo-server's response line names os.Hostname(). Detaching ends the
// handler and hands the workload back to the cluster.
func (s *DockerRun) Test_HandlerServesIntercept() {
	t := s.T()
	ctx := s.Ctx()
	conn := s.Connect()
	wl := s.Workload(workloads.Echo("docker-run"))

	args := []string{
		"intercept", wl.Name,
		"--namespace", wl.Namespace,
		"--mount", "false",
		"--port", fmt.Sprintf("%d:%d", dockerRunLocalPort, dockerRunContainerPort),
		"--docker-run", "--",
		"--rm", "--hostname", dockerRunHostname, "--name", dockerRunContainerName,
		dockerRunImage,
	}

	tp := s.CLI()
	cmd := exec.CommandContext(ctx, tp.Exe, args...)
	cmd.Env = tp.Env
	cmd.Dir = tp.Dir
	s.Require().NoError(cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	detached := false
	t.Cleanup(func() {
		if detached {
			return
		}
		_, _, _ = s.CLI().Run(ctx, "detach", wl.Name, "-n", wl.Namespace)
		select {
		case <-done:
		case <-time.After(dockerRunExitTimeout):
			t.Log("docker-run handler process did not exit during cleanup")
		}
	})
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", dockerRunContainerName).Run() })

	s.Eventually(func() bool {
		return attached(conn.List(t), wl.Name, wl.Namespace)
	}, dockerRunAttachTimeout, time.Second, "docker-run intercept did not appear in list")

	url := wl.ServiceURL()
	check.EventuallyHTTP(t, url, servedByHostname(dockerRunHostname), dockerRunRouteTimeout)

	_, stderr, err := s.CLI().Run(ctx, "detach", wl.Name, "-n", wl.Namespace)
	s.Require().NoError(err, "detach: %s", stderr)
	detached = true

	select {
	case werr := <-done:
		if werr != nil {
			t.Logf("docker-run handler process exited with: %v", werr)
		}
	case <-time.After(dockerRunExitTimeout):
		t.Fatalf("docker-run handler did not exit after detach")
	}

	check.EventuallyHTTP(t, url, notServedByHostname(dockerRunHostname), dockerRunRouteTimeout)
}

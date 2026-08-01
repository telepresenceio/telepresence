package docker

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	// runLifecycleLocalPort is the local half of the intercept's --port
	// <local>:<container> pair; dockerRunContainerPort (docker_run.go) is
	// the container half, the workload's own container port.
	runLifecycleLocalPort = 9071

	// runLifecycleHostname/runLifecycleContainerName identify this suite's
	// handler container. Distinct from docker_run.go's DockerRun suite so
	// the two never share a docker container name.
	runLifecycleHostname      = "rtest-runlifecycle-handler"
	runLifecycleContainerName = "rtest-runlifecycle-handler"

	// runLifecycleTeardownTimeout bounds every teardown's cli.Proc.Wait:
	// the docker-run handler process must exit within 10s of each of the
	// four teardown methods.
	runLifecycleTeardownTimeout = 10 * time.Second
)

// RunLifecycle is the four-way teardown matrix for `intercept --docker-run`
// on the shared host daemon: docker_run_test.go's Test_DockerRun_HostDaemon.
// Each subtest starts a fresh handler (the published echo-server image,
// docker_run.go's conventions) against the same workload, waits for cluster
// traffic to reach it, then ends the attachment a different way -- SIGINT,
// `detach`, `disconnect` (a session-only `quit`), and `quit -s` (all local
// daemons) -- asserting the CLI process behind --docker-run (cli.TP.Start's
// Proc) always exits within 10s and traffic reverts to the cluster pod
// afterward. Runs `-i`, never `-t`: a TTY changes how the child process
// handles signals.
type RunLifecycle struct {
	rt.Suite
}

func init() {
	rt.Register(&RunLifecycle{},
		rt.InArea("docker"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// runLifecycleInterceptIDPattern matches echo-server's "Intercept id
// <uuid>:<workload>[/<container>]" response line (docs/howtos/docker.md's
// example), which the image only prints when TELEPRESENCE_INTERCEPT_ID
// reached its environment: proof that --docker-run's unconditional
// --env-file (pkg/client/cli/docker/runner.go's Runner.Run) actually
// delivered the intercept's environment into the container, not just that
// some container answered.
func runLifecycleInterceptIDPattern(wl *rt.Workload) *regexp.Regexp {
	return regexp.MustCompile(`Intercept id [0-9a-f-]+:` + regexp.QuoteMeta(wl.Name))
}

// runLifecycleServedWithEnv is the "response identity" predicate: the body
// must carry both this suite's handler's --hostname marker (proving the
// cluster pod isn't answering) and the intercept-id line above (proving the
// env file reached it).
func runLifecycleServedWithEnv(wl *rt.Workload) func(status int, body string) bool {
	hostMarker := "Request served by " + runLifecycleHostname
	idPattern := runLifecycleInterceptIDPattern(wl)
	return func(status int, body string) bool {
		return status == http.StatusOK && strings.Contains(body, hostMarker) && idPattern.MatchString(body)
	}
}

// runLifecycleArgs builds the `intercept --docker-run` argv, mirroring
// docker_run.go's DockerRun suite's image/args conventions with this
// suite's own hostname/container-name constants.
func runLifecycleArgs(wl *rt.Workload) []string {
	return []string{
		"intercept", wl.Name,
		"--namespace", wl.Namespace,
		"--mount", "false",
		"--port", fmt.Sprintf("%d:%d", runLifecycleLocalPort, dockerRunContainerPort),
		"--docker-run", "--",
		"--rm", "-i", "--hostname", runLifecycleHostname, "--name", runLifecycleContainerName,
		dockerRunImage,
	}
}

// startHandler starts this subtest's docker-run handler in the background
// (cli.TP.Start) and registers a container cleanup, so a subtest that fails
// before reaching its own teardown still leaves no container behind.
func (s *RunLifecycle) startHandler(wl *rt.Workload) *cli.Proc {
	t := s.T()
	t.Helper()
	ctx := s.Ctx()
	_ = exec.Command("docker", "rm", "-f", runLifecycleContainerName).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", runLifecycleContainerName).Run() })
	p, err := s.CLI().Start(ctx, runLifecycleArgs(wl)...)
	s.Require().NoError(err, "starting docker-run handler")
	return p
}

// awaitServing waits for wl's intercept to show up in list and for its
// service URL to be answered by this subtest's handler, with the env-file
// proof in the body.
func (s *RunLifecycle) awaitServing(conn *rt.Conn, wl *rt.Workload) {
	t := s.T()
	t.Helper()
	s.Eventually(func() bool { return attached(conn.List(t), wl.Name, wl.Namespace) },
		dockerRunAttachTimeout, attachPollInterval, "docker-run intercept did not appear in list")
	check.EventuallyHTTP(t, wl.ServiceURL(), runLifecycleServedWithEnv(wl), dockerRunRouteTimeout)
}

// awaitGone waits for wl's service URL to revert to the cluster pod, no
// longer answered by this subtest's handler hostname.
func (s *RunLifecycle) awaitGone(wl *rt.Workload) {
	t := s.T()
	t.Helper()
	check.EventuallyHTTP(t, wl.ServiceURL(), notServedByHostname(runLifecycleHostname), dockerRunRouteTimeout)
}

// Test_Teardown starts the same docker-run handler four times against one
// workload over the shared host connection, tearing it down a different way
// each time.
func (s *RunLifecycle) Test_Teardown() {
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("run-lifecycle"))

	s.Run("sigint", func() {
		conn := s.Connect()
		p := s.startHandler(wl)
		s.awaitServing(conn, wl)

		s.Require().NoError(p.Signal(os.Interrupt))
		waitProcExit(s.T(), p, runLifecycleTeardownTimeout)
		s.awaitGone(wl)
	})

	s.Run("detach", func() {
		ctx := s.Ctx()
		conn := s.Connect()
		p := s.startHandler(wl)
		s.awaitServing(conn, wl)

		_, stderr, err := s.CLI().Run(ctx, "detach", wl.Name, "-n", wl.Namespace)
		s.Require().NoError(err, "detach: %s", stderr)
		waitProcExit(s.T(), p, runLifecycleTeardownTimeout)
		s.awaitGone(wl)
	})

	s.Run("disconnect", func() {
		t := s.T()
		ctx := s.Ctx()
		conn := s.Connect()
		p := s.startHandler(wl)
		s.awaitServing(conn, wl)

		// disconnect: a session-only `quit` (no -s), leaving the daemon
		// process running but the session gone.
		_, stderr, err := s.CLI().Run(ctx, "quit")
		s.Require().NoError(err, "quit: %s", stderr)
		waitProcExit(t, p, runLifecycleTeardownTimeout)

		rt.Reconnect(t, ctx, ns)
		s.awaitGone(wl)
	})

	s.Run("quit", func() {
		t := s.T()
		ctx := s.Ctx()
		// Mutate: `quit -s` below churns the shared connection, so later
		// tests must not adopt whatever it leaves memoized -- go through
		// the fixture's own invalidation instead of a bare CLI call
		// (coexist.go's connections follow the same rule).
		conn := rt.Mutate(t, rt.ConnectionFixture(ns))
		p := s.startHandler(wl)
		s.awaitServing(conn, wl)

		conn.Disconnect(t) // quit -s: stops every local daemon.
		waitProcExit(t, p, runLifecycleTeardownTimeout)

		rt.Reconnect(t, ctx, ns)
		s.awaitGone(wl)
	})
}

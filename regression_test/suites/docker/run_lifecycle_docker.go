package docker

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	// runLifecycleDockerConnName is the named `--docker` connection this
	// suite's intercept runs over (conn_run.go's rt.ConnNamed/rt.ConnDocker
	// idiom), distinct from conn_run.go's own connRunName so the two suites
	// never share a daemon container.
	runLifecycleDockerConnName = "rtest-runlifecycle-docker"

	// runLifecycleDockerPort is the intercept's --port value. For a
	// containerized daemon the flag form is <local-port>[:<svcPortIdentifier>]
	// -- parsePort (pkg/client/cli/intercept/state.go) rejects the
	// three-part <local>:<container>:<id> form outright and reads a second
	// element as a service-port identifier, never a container port. The
	// daemon delivers intercepted traffic by dialing the handler container,
	// a peer on its own docker network, at this local port, so it must be
	// the port the handler image actually listens on
	// (dockerRunContainerPort, the echo image's default).
	runLifecycleDockerPort = dockerRunContainerPort

	// runLifecycleDockerHostname/runLifecycleDockerContainerName identify
	// this suite's handler container, distinct from run_lifecycle.go's and
	// docker_run.go's so the three never share a docker container name.
	runLifecycleDockerHostname      = "rtest-runlifecycle-docker-handler"
	runLifecycleDockerContainerName = "rtest-runlifecycle-docker-handler"
)

// RunLifecycleDocker runs run_lifecycle.go's RunLifecycle four-way teardown
// matrix (SIGINT, detach, disconnect, quit) over a containerized daemon
// (conn_run.go's named `--docker` connection idiom) instead of the shared
// host daemon.
//
// Every per-connection CLI call addresses the connection with --use: without
// it, `quit`/`detach` resolve an identifier from the current kube-context
// and namespace (pkg/client/cli/daemon/identifier.go's IdentifierFromFlags),
// not this suite's explicit --name.
//
// The containerized daemon manages one session for its own process lifetime
// (pkg/client/userd/daemon/service.go's rootSessionInProc field, set from
// the embedded-network flag): once its session ends, the daemon quits
// itself (pkg/client/userd/daemon/grpc.go's cancelSession: `if
// s.rootSessionInProc { s.quit(false) }`, reached from both Quit and
// Disconnect). The host daemon has no such field and outlives a session-only
// disconnect. Neither the disconnect nor the quit subtest below uses `-s`:
// `-s` finds and quits every local daemon it can locate on disk regardless
// of --use (pkg/client/cli/connect/connector.go's QuitDaemonFuncs:
// quitHostConnector plus quitDockerDaemons), which would also stop any other
// connection -- host or another named docker daemon -- already running.
type RunLifecycleDocker struct {
	rt.Suite
}

func init() {
	rt.Register(&RunLifecycleDocker{},
		rt.InArea("docker"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// connect returns this suite's named docker connection, provisioning it on
// first use.
func (s *RunLifecycleDocker) connect() *rt.Conn {
	return s.Connect(rt.ConnNamed(runLifecycleDockerConnName), rt.ConnDocker())
}

// reconnect restores this suite's named docker connection after a teardown
// that ended its session, and so its daemon container.
func (s *RunLifecycleDocker) reconnect(ctx context.Context, ns string) {
	rt.Reconnect(s.T(), ctx, ns, rt.ConnNamed(runLifecycleDockerConnName), rt.ConnDocker())
}

// runLifecycleDockerArgs builds the `intercept --docker-run` argv:
// run_lifecycle.go's runLifecycleArgs shape, addressed at this suite's named
// docker connection with --use and this suite's own hostname/container-name
// constants.
func runLifecycleDockerArgs(wl *rt.Workload) []string {
	return []string{
		"intercept", wl.Name,
		"--namespace", wl.Namespace,
		"--use", runLifecycleDockerConnName,
		"--mount", "false",
		"--port", fmt.Sprintf("%d", runLifecycleDockerPort),
		"--docker-run", "--",
		"--rm", "-i", "--hostname", runLifecycleDockerHostname, "--name", runLifecycleDockerContainerName,
		dockerRunImage,
	}
}

// startHandler starts this subtest's docker-run handler in the background
// and registers a container cleanup: run_lifecycle.go's startHandler.
func (s *RunLifecycleDocker) startHandler(wl *rt.Workload) *cli.Proc {
	t := s.T()
	t.Helper()
	ctx := s.Ctx()
	_ = exec.Command("docker", "rm", "-f", runLifecycleDockerContainerName).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", runLifecycleDockerContainerName).Run() })
	p, err := s.CLI().Start(ctx, runLifecycleDockerArgs(wl)...)
	s.Require().NoError(err, "starting docker-run handler")
	return p
}

// clusterProbe fetches wl's service from inside the cluster -- kubectl exec
// into the workload's own app container, wget against the service name --
// the one vantage that sees an intercept the way real in-cluster traffic
// does. Neither the host nor a docker-network container can stand in for
// it here: the host has no tunnel of its own for a docker connection, and
// a container attached to the daemon's network shares its DNS but not the
// network namespace that holds the TUN, so cluster addresses don't route
// from there.
func (s *RunLifecycleDocker) clusterProbe(wl *rt.Workload) (string, error) {
	args := []string{
		"exec", "deploy/" + wl.Name, "-c", wl.Name, "--",
		"wget", "-q", "-O", "-", "-T", "2", "http://" + net.JoinHostPort(wl.SvcName, strconv.Itoa(wl.Port)),
	}
	out, err := s.R().Kubectl(s.Ctx(), wl.Namespace, args...)
	if err != nil {
		return "", fmt.Errorf("kubectl exec wget: %w: %s", err, out)
	}
	return out, nil
}

// awaitServing waits for wl's intercept to show up in list and for its
// service URL, probed through the daemon's own network, to be answered by
// this subtest's handler with the env-file proof in the body:
// run_lifecycle.go's awaitServing, addressed at the containerized daemon.
// probeUntil polls wl's service from inside the cluster until want accepts
// the body, failing t with the last error and body when the timeout runs
// out -- a probe's terminal state is only diagnosable from what it actually
// got back.
func (s *RunLifecycleDocker) probeUntil(wl *rt.Workload, what string, want func(status int, body string) bool) {
	t := s.T()
	t.Helper()
	deadline := time.Now().Add(dockerRunRouteTimeout)
	var lastBody string
	var lastErr error
	for time.Now().Before(deadline) {
		lastBody, lastErr = s.clusterProbe(wl)
		if lastErr == nil && want(200, lastBody) {
			return
		}
		time.Sleep(attachPollInterval)
	}
	t.Fatalf("%s: %s never satisfied (last err: %v, last body: %q)",
		wl.SvcName, what, lastErr, lastBody)
}

// awaitServing waits for wl's intercept to show up in list and for its
// service, probed from inside the cluster, to be answered by this subtest's
// handler with the env-file proof in the body: run_lifecycle.go's
// awaitServing, addressed at the containerized daemon.
func (s *RunLifecycleDocker) awaitServing(conn *rt.Conn, wl *rt.Workload) {
	t := s.T()
	t.Helper()
	s.Eventually(func() bool { return attached(conn.List(t), wl.Name, wl.Namespace) },
		dockerRunAttachTimeout, attachPollInterval, "docker-run intercept did not appear in list")
	s.probeUntil(wl, "handler serving with env proof", runLifecycleServedWithEnv(wl, runLifecycleDockerHostname))
}

// awaitGone waits for wl's service, probed from inside the cluster, to
// revert to the cluster pod: run_lifecycle.go's awaitGone.
func (s *RunLifecycleDocker) awaitGone(wl *rt.Workload) {
	s.T().Helper()
	s.probeUntil(wl, "reverted to the cluster pod", notServedByHostname(runLifecycleDockerHostname))
}

// Test_Teardown starts the same docker-run handler four times against one
// workload over this suite's named docker connection, tearing it down a
// different way each time: run_lifecycle.go's Test_Teardown, addressed at
// the containerized daemon instead of the shared host daemon.
func (s *RunLifecycleDocker) Test_Teardown() {
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("run-lifecycle-docker"))
	name := runLifecycleDockerConnName

	s.Run("sigint", func() {
		conn := s.connect()
		p := s.startHandler(wl)
		s.awaitServing(conn, wl)

		s.Require().NoError(p.Signal(os.Interrupt))
		waitProcExit(s.T(), p, runLifecycleTeardownTimeout)
		s.awaitGone(wl)
	})

	s.Run("detach", func() {
		ctx := s.Ctx()
		conn := s.connect()
		p := s.startHandler(wl)
		s.awaitServing(conn, wl)

		_, stderr, err := s.CLI().Run(ctx, "detach", wl.Name, "-n", wl.Namespace, "--use", name)
		s.Require().NoError(err, "detach: %s", stderr)
		waitProcExit(s.T(), p, runLifecycleTeardownTimeout)
		s.awaitGone(wl)
	})

	s.Run("disconnect", func() {
		t := s.T()
		ctx := s.Ctx()
		conn := s.connect()
		p := s.startHandler(wl)
		s.awaitServing(conn, wl)

		// A session-only `quit --use <name>` (no -s). Unlike the host
		// daemon, the containerized daemon quits itself once its one
		// session ends (rootSessionInProc), so this also ends the daemon
		// container -- but only this one, unlike -s.
		_, stderr, err := s.CLI().Run(ctx, "quit", "--use", name)
		s.Require().NoError(err, "quit --use %s: %s", name, stderr)
		waitProcExit(t, p, runLifecycleTeardownTimeout)

		// The daemon container quit out of band from the fixture engine's
		// own Destroy/Mutate path, so the memoized connection fixture must
		// be forgotten before it is relied on again (fixture_connection.go's
		// ForgetConnections).
		s.R().ForgetConnections()
		s.reconnect(ctx, ns)
		s.awaitGone(wl)
	})

	s.Run("quit", func() {
		t := s.T()
		ctx := s.Ctx()
		conn := s.connect()
		p := s.startHandler(wl)
		s.awaitServing(conn, wl)

		conn.Disconnect(t) // quit --use <name>: this connection's own daemon.
		waitProcExit(t, p, runLifecycleTeardownTimeout)

		s.R().ForgetConnections()
		s.reconnect(ctx, ns)
		s.awaitGone(wl)
	})
}

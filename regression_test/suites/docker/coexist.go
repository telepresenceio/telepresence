package docker

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// coexistHostName/coexistDockerName name the two connections both tests
// below establish: a plain host connection and a docker connection. Naming
// both (rather than leaving the host one the default, unnamed connection)
// means every per-connection call in this suite is --use-scoped, so it stays
// unambiguous even while the other connection is also live.
const (
	coexistHostName   = "rtest-coexist-host"
	coexistDockerName = "rtest-coexist-docker"
)

// Coexist proves a host (non-docker) connection and a docker connection can
// be live at the same time, regardless of which is established first:
// docker_daemon_test.go's hostDaemonNoConflict/daemonHostNotConflict
// essentials, plus a concurrent list from both and a check that the host
// connection survives the docker one quitting.
type Coexist struct {
	rt.Suite
}

func init() {
	rt.Register(&Coexist{},
		rt.InArea("docker"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// coexistListResult pairs the entries a concurrent list call returned with
// any error, so the host and docker probes below can run on separate
// goroutines without calling a *testing.T failure method off the test's own
// goroutine (testing.T.FailNow's documented single-goroutine contract).
type coexistListResult struct {
	entries []cli.ListEntry
	err     error
}

// listWorkloads runs `telepresence list --format json --use <use>` and
// reports the result on ch.
func listWorkloads(ctx context.Context, tp *cli.TP, use string, ch chan<- coexistListResult) {
	var entries []cli.ListEntry
	err := tp.JSON(ctx, &entries, "list", "--format", "json", "--use", use)
	ch <- coexistListResult{entries: entries, err: err}
}

// concurrentList runs a host list (--use hostName) and a docker list (--use
// dockerName) at the same time, returning both once complete.
func concurrentList(
	ctx context.Context, tp *cli.TP, hostName, dockerName string,
) (host, dockerConn coexistListResult) {
	hostCh := make(chan coexistListResult, 1)
	dockerCh := make(chan coexistListResult, 1)
	go listWorkloads(ctx, tp, hostName, hostCh)
	go listWorkloads(ctx, tp, dockerName, dockerCh)
	return <-hostCh, <-dockerCh
}

// Test_HostThenDocker establishes the named host connection first, then the
// named docker connection second. Both list their namespace concurrently,
// each --use-scoped to its own name so the call stays unambiguous with two
// connections live; quitting the docker one leaves the host connection
// listing.
func (s *Coexist) Test_HostThenDocker() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("coexist-echo"))

	host := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnNamed(coexistHostName)))
	defer host.Disconnect(t)

	docker := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnNamed(coexistDockerName), rt.ConnDocker()))

	hostRes, dockerRes := concurrentList(ctx, s.CLI(), coexistHostName, coexistDockerName)
	s.Require().NoError(hostRes.err, "host list")
	s.Require().NoError(dockerRes.err, "docker list")
	s.True(present(hostRes.entries, wl.Name, wl.Namespace), "host connection should list %s", wl.Name)
	s.True(present(dockerRes.entries, wl.Name, wl.Namespace), "docker connection should list %s", wl.Name)

	docker.Disconnect(t)
	s.True(present(host.List(t), wl.Name, wl.Namespace),
		"host connection should still list %s after the docker one quit", wl.Name)
}

// Test_DockerThenHost is Test_HostThenDocker with establishment order
// reversed: the named docker connection first, then the named host
// connection second. A named host connect only quits a same-named stale
// session (fixture_connection.go's ensureHostDaemon), never the broad
// `quit -s`, so it never disturbs the docker connection just established.
func (s *Coexist) Test_DockerThenHost() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	wl := s.Workload(workloads.Echo("coexist-echo"))

	docker := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnNamed(coexistDockerName), rt.ConnDocker()))

	host := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnNamed(coexistHostName)))
	defer host.Disconnect(t)

	hostRes, dockerRes := concurrentList(ctx, s.CLI(), coexistHostName, coexistDockerName)
	s.Require().NoError(hostRes.err, "host list")
	s.Require().NoError(dockerRes.err, "docker list")
	s.True(present(hostRes.entries, wl.Name, wl.Namespace), "host connection should list %s", wl.Name)
	s.True(present(dockerRes.entries, wl.Name, wl.Namespace), "docker connection should list %s", wl.Name)

	docker.Disconnect(t)
	s.True(present(host.List(t), wl.Name, wl.Namespace),
		"host connection should still list %s after the docker one quit", wl.Name)
}

package connect

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/ioutil"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// inactiveBlockTimeout/inactivePingInterval are the manager/client values
// inactiveClientSpec and pingFast configure. cmd/traffic/cmd/manager/state/
// intercept.go's checkInterceptConflicts only overrides a conflicting
// intercept once the blocking client's activity mark (cmd/traffic/cmd/
// manager/state/session.go's lastMarked) is older than managerutil.Env's
// InterceptInactiveBlockTimeout, sourced from the chart's
// intercept.inactiveBlockTimeout (inactiveClientSpec below).
//
// The mark tracks the CLIENT's own activity, not the daemon's liveness: the
// daemon's remainLoop pings the manager on every Grpc().PingInterval tick,
// but each Remain reports the client's own last user activity
// (pkg/client/userd/trafficmgr/session.go's remain(), which sends
// RemainRequest.LastActivity from s.lastActivity; cmd/traffic/cmd/manager/
// service.go's Remain marks the session with that reported time). A
// connected-but-idle client's mark therefore goes stale on its own, which
// is what Test_ConflictOverrideInactive relies on; the fast ping only makes
// sure the manager hears about activity (or the lack of it) promptly.
const (
	inactiveBlockTimeout = 10 * time.Second
	inactivePingInterval = 2 * time.Second
	inactiveTakeoverWait = inactiveBlockTimeout + inactivePingInterval
)

// containerRemovalTimeout/containerRemovalInterval bound the wait for a
// quit daemon's container to disappear.
const (
	containerRemovalTimeout  = 30 * time.Second
	containerRemovalInterval = time.Second
)

// containerGone polls until no container named name remains, reporting
// whether it disappeared within containerRemovalTimeout.
func containerGone(name string) bool {
	deadline := time.Now().Add(containerRemovalTimeout)
	for {
		out, err := exec.Command("docker", "ps", "-aq", "-f", "name=^"+name+"$").Output()
		if err == nil && strings.TrimSpace(string(out)) == "" {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(containerRemovalInterval)
	}
}

// inactiveClientSpec overlays intercept.inactiveBlockTimeout (the chart's
// 10m default) down to inactiveBlockTimeout, so Test_ConflictOverride*
// don't wait out the production default. Built inline rather than added to
// the catalog since no other suite needs it.
//
//nolint:gochecknoglobals // registered once at init time, like every other manager spec
var inactiveClientSpec = managers.Spec{
	Key: "inactive-block-timeout",
	Values: managers.Values{
		Intercept: managers.Intercept{InactiveBlockTimeout: inactiveBlockTimeout.String()},
	},
}

// pingFast is a ConnWithConfig delta setting grpc.pingInterval
// (pkg/client/config.go's Grpc.PingInterval) down to inactivePingInterval,
// so a connected daemon's remainLoop reports activity often enough that
// inactiveTakeoverWait can stay short.
func pingFast(cfg client.Config) {
	cfg.Grpc().PingInterval = inactivePingInterval
}

// conflictHeader is the x-user=adam idiom both connections' intercepts
// filter on. Identical filters are a subset of each other, the simplest
// case pkg/icept/conflicts.go's IsInConflict/ExplainConflict report as
// "header filters overlap".
//
//nolint:gochecknoglobals // constant test fixture, not mutated
var conflictHeader = [2]string{"x-user", "adam"}

// InactiveClient proves that a client blocking a conflicting
// header-filtered intercept loses that veto once its activity mark goes
// stale: two named docker connections attempt the same x-user=adam
// intercept on one workload. The second is rejected ("header filters
// overlap") while the first client is active, and succeeds -- overriding
// the first -- once the first has been quiet past
// intercept.inactiveBlockTimeout. Test_ConflictOverrideInactive keeps the
// first daemon running but idle; Test_ConflictOverrideSleeping freezes it
// outright with docker pause.
type InactiveClient struct {
	rt.Suite
}

func init() {
	rt.Register(&InactiveClient{},
		rt.InArea("connect"),
		rt.NeedsManager(inactiveClientSpec),
		rt.Requires(rt.Docker),
		rt.On("linux"),
	)
}

// conflictPair is what setupConflict hands the two takeover variants: the
// established loser/winner connections plus the names each variant's own
// assertions address.
type conflictPair struct {
	nameOne      string
	containerOne string
	interceptOne string
	interceptTwo string
	connTwo      *rt.Conn
	wl           *rt.Workload
	lsTwo        *rt.LocalService
}

// setupConflict provisions the workload and both named docker connections,
// takes the header-filtered intercept via the first connection, and proves
// the second connection's identical attempt is rejected with the
// header-overlap conflict while the first client is active. Cleanups
// tolerate a daemon that has already ended (the sleeping variant's loser
// terminates itself), and the last-registered cleanup drops every memoized
// connection, since a quit here happens out of band from the fixture
// engine.
func (s *InactiveClient) setupConflict(nameOne, nameTwo string) conflictPair {
	t := s.T()
	ctx := s.Ctx()
	s.Manager()

	// daemon.Identifier.ContainerName()/InfoFileName(): for a --name'd,
	// --docker connection, both are exactly ioutil.SafeName(name), which is
	// a no-op on the plain lowercase-and-hyphen names used here.
	containerOne := ioutil.SafeName(nameOne)
	containerTwo := ioutil.SafeName(nameTwo)
	t.Cleanup(func() {
		// Runs last (Cleanup is LIFO): both connections have been quit by
		// then, and docker run --rm (pkg/client/docker/daemon.go's
		// LaunchDaemon) removes a container once its daemon process exits.
		// That removal is asynchronous -- the daemon's exit and the
		// container's disappearance are separate events -- so poll instead
		// of reading once.
		for _, c := range []string{containerOne, containerTwo} {
			if !containerGone(c) {
				t.Errorf("container %s still exists %s after disconnect", c, containerRemovalTimeout)
			}
		}
	})
	t.Cleanup(rt.R().ForgetConnections)

	wl := s.Workload(workloads.Echo("echo-" + nameOne))
	lsOne := s.LocalEcho()
	lsTwo := s.LocalEcho()

	interceptOne := nameOne + "-ic"
	interceptTwo := nameTwo + "-ic"

	connOne := s.Connect(rt.ConnNamed(nameOne), rt.ConnDocker(), rt.ConnWithConfig(pingFast))
	t.Cleanup(func() { quitTolerant(t, s.Ctx(), nameOne) })

	connTwo := s.Connect(rt.ConnNamed(nameTwo), rt.ConnDocker(), rt.ConnWithConfig(pingFast))
	t.Cleanup(func() { quitTolerant(t, s.Ctx(), nameTwo) })

	// First intercept, via connOne. Never detached explicitly: it is the
	// intercept the takeover overrides.
	connOne.InterceptNamed(t, interceptOne, wl,
		rt.ToLocal(lsOne, "http"), cli.MountFalse(), cli.HTTPHeader(conflictHeader[0], conflictHeader[1]))

	// Second, identical attempt via connTwo: must fail while connOne is
	// active. Conn.InterceptNamed calls t.Fatalf on a non-zero exit, which
	// an attempt expecting failure can't use, so this runs the CLI
	// directly.
	stdout, stderr, err := s.CLI().Run(ctx, conflictAttemptArgs(connTwo, interceptTwo, wl, lsTwo)...)
	if err == nil {
		_, _, _ = s.CLI().Run(ctx, "detach", interceptTwo, "-n", wl.Namespace, "--use", connTwo.Name())
		t.Fatalf("expected a same-header intercept from a second, active client to fail")
	}
	combined := strings.ToLower(stdout + stderr)
	if !strings.Contains(combined, "header filters overlap") {
		t.Fatalf("expected a header-filter overlap conflict, got stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	return conflictPair{
		nameOne:      nameOne,
		containerOne: containerOne,
		interceptOne: interceptOne,
		interceptTwo: interceptTwo,
		connTwo:      connTwo,
		wl:           wl,
		lsTwo:        lsTwo,
	}
}

// conflictAttemptArgs builds the raw CLI argv for connTwo's conflicting
// intercept attempt.
func conflictAttemptArgs(connTwo *rt.Conn, interceptTwo string, wl *rt.Workload, lsTwo *rt.LocalService) []string {
	args := make([]string, 0, 16)
	args = append(args,
		"intercept", interceptTwo, "--namespace", wl.Namespace, "--format", "json",
		"--workload", wl.Name,
	)
	for _, o := range []cli.InterceptOpt{
		rt.ToLocal(lsTwo, "http"), cli.MountFalse(), cli.HTTPHeader(conflictHeader[0], conflictHeader[1]),
	} {
		args = append(args, o()...)
	}
	if n := connTwo.Name(); n != "" {
		args = append(args, "--use", n)
	}
	return args
}

// quitTolerant quits the named connection's daemon, tolerating one that has
// already ended (the sleeping variant's loser terminates itself on resume).
func quitTolerant(t testing.TB, ctx context.Context, name string) {
	t.Helper()
	if _, stderr, err := rt.R().CLI().Run(ctx, "quit", "--use", name); err != nil {
		t.Logf("[rtest] InactiveClient: quit --use %s: %v: %s", name, err, stderr)
	}
}

// Test_ConflictOverrideInactive is the running-but-idle takeover: after the
// failed attempt, nothing touches the first connection, so its daemon keeps
// pinging while its client-activity mark stays put. Once the mark is older
// than intercept.inactiveBlockTimeout, the second attempt succeeds, and the
// first connection -- whose daemon is still alive -- sees its own intercept
// in AGENT_ERROR naming the winner.
func (s *InactiveClient) Test_ConflictOverrideInactive() {
	t := s.T()
	ctx := s.Ctx()
	p := s.setupConflict("inactive-one", "inactive-two")

	time.Sleep(inactiveTakeoverWait)

	a2 := p.connTwo.InterceptNamed(t, p.interceptTwo, p.wl,
		rt.ToLocal(p.lsTwo, "http"), cli.MountFalse(), cli.HTTPHeader(conflictHeader[0], conflictHeader[1]))
	defer a2.Detach(t)

	// checkInterceptConflicts marks the loser AGENT_ERROR with a message
	// naming the winner. `list`'s default text renderer shows
	// "<Disposition>: <Message>" (pkg/client/cli/intercept/info.go's
	// WriteTo); the JSON-typed cli.ListEntry carries neither field, hence
	// the raw CLI call.
	conflictRe := regexp.MustCompile(`AGENT_ERROR: conflict with intercept [\w-]+:` + regexp.QuoteMeta(p.interceptTwo))
	s.Eventually(func() bool {
		out, _, err := s.CLI().Run(ctx, "list", "--intercepts", "--use", p.nameOne)
		return err == nil && conflictRe.MatchString(out)
	}, attachTimeout, time.Second, "connOne's intercept never showed the AGENT_ERROR override")
}

// Test_ConflictOverrideSleeping is the frozen-client takeover: the first
// connection's daemon is paused outright (the laptop-went-to-sleep shape)
// and the second attempt succeeds while it is still frozen. What the
// resumed loser looks like afterwards is timing-dependent: streams that
// broke during the freeze can either reconnect -- the daemon lives on and
// lists its overridden intercept -- or end its session, and a
// containerized daemon then ends with it
// (pkg/client/userd/daemon/service.go's rootSessionInProc, applied by
// grpc.go's cancelSession), taking its container along. Both are terminal
// for the block, and the final assertion accepts whichever the resume
// produced -- what it never accepts is the loser still holding a live,
// blocking intercept.
func (s *InactiveClient) Test_ConflictOverrideSleeping() {
	t := s.T()
	p := s.setupConflict("sleeping-one", "sleeping-two")

	infoFile := p.containerOne + ".json"
	infoPath := filepath.Join(rt.R().UserCacheDir(), "userd", infoFile)
	if _, err := os.Stat(infoPath); err != nil {
		t.Fatalf("daemon info file %s: %v", infoPath, err)
	}

	// The winner's own --use resolution enumerates every daemon-info file
	// under userd/ and reaps any older than 3*keepAliveInterval=6s
	// (pkg/client/cli/daemon/info.go's LoadInfos/deleteIfStale, well inside
	// inactiveTakeoverWait), so the frozen daemon's entry is kept fresh by
	// hand while its own process cannot. KeepInfoAlive deletes the file
	// when its context ends -- correct cleanup here, since by then the
	// paused daemon is gone for good.
	kaCtx, kaCancel := context.WithCancel(s.Ctx())
	t.Cleanup(kaCancel)
	go func() {
		if err := daemon.NewUserInfoLoader(kaCtx).KeepInfoAlive(infoFile); err != nil {
			rt.R().Infof("[rtest] keep-alive for %s: %v", infoFile, err)
		}
	}()

	if out, err := exec.Command("docker", "pause", p.containerOne).CombinedOutput(); err != nil {
		t.Fatalf("docker pause %s: %v: %s", p.containerOne, err, out)
	}
	t.Cleanup(func() {
		// Best-effort: a failure before the resume below must not leave the
		// container frozen. Unpausing a non-paused container just errors.
		_ = exec.Command("docker", "unpause", p.containerOne).Run()
	})

	assertPausedAndKeptAlive(t, p.containerOne, infoFile)

	time.Sleep(inactiveTakeoverWait)

	// The takeover lands while the loser is still frozen.
	a2 := p.connTwo.InterceptNamed(t, p.interceptTwo, p.wl,
		rt.ToLocal(p.lsTwo, "http"), cli.MountFalse(), cli.HTTPHeader(conflictHeader[0], conflictHeader[1]))
	defer a2.Detach(t)

	if out, err := exec.Command("docker", "unpause", p.containerOne).CombinedOutput(); err != nil {
		t.Fatalf("docker unpause %s: %v: %s", p.containerOne, err, out)
	}

	conflictRe := regexp.MustCompile(`AGENT_ERROR: conflict with intercept [\w-]+:` + regexp.QuoteMeta(p.interceptTwo))
	s.Eventually(func() bool {
		out, err := exec.Command("docker", "ps", "-q", "-f", "name=^"+p.containerOne+"$").Output()
		if err == nil && strings.TrimSpace(string(out)) == "" {
			return true // the daemon ended with its session; container gone
		}
		lo, _, lerr := s.CLI().Run(s.Ctx(), "list", "--intercepts", "--use", p.nameOne)
		if lerr != nil {
			return false // daemon mid-shutdown, or not answering yet
		}
		// The surviving daemon's own view: its intercept is either marked
		// with the override, or already reaped from its list entirely.
		return conflictRe.MatchString(lo) || !strings.Contains(lo, p.interceptOne)
	}, attachTimeout, time.Second,
		"the resumed loser should end, show AGENT_ERROR, or lose its intercept -- never still hold the block")
}

// assertPausedAndKeptAlive proves the docker-pause harness technique
// itself: the container actually reports paused, and its daemon-info
// file's mtime keeps advancing during the freeze -- which can only be
// the test's stand-in KeepInfoAlive goroutine, since the real daemon's own
// equivalent goroutine is frozen along with every other process in the
// container.
func assertPausedAndKeptAlive(t *testing.T, container, infoFile string) {
	t.Helper()

	out, err := exec.Command("docker", "inspect", "-f", "{{.State.Paused}}", container).Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("expected %s to report paused, got %q, err %v", container, out, err)
	}

	path := filepath.Join(rt.R().UserCacheDir(), "userd", infoFile)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("daemon info file %s: %v", path, err)
	}
	time.Sleep(3 * time.Second) // > KeepInfoAlive's 2s refresh interval
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("daemon info file %s: %v", path, err)
	}
	if !after.ModTime().After(before.ModTime()) {
		t.Fatalf("daemon info file %s: mtime did not advance while %s was paused", path, container)
	}
}

package connect

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	// networkDownTimeout bounds how long the cluster stays reachable after
	// the user daemon was killed, and how long the daemons take to exit
	// after `quit -s`.
	networkDownTimeout = 30 * time.Second
	pollInterval       = 500 * time.Millisecond

	// staleInfoTimeout bounds how long a killed user daemon's info file
	// keeps connect from starting a new user daemon.
	staleInfoTimeout = 20 * time.Second
)

// SessionOwner proves that the root daemon's session lives and dies with the
// user daemon that created it, and that `quit -s` stops both daemons. The
// tests claim the default connection and clean up with `quit -s`. Linux only:
// the user daemon's pid is looked up with ss.
type SessionOwner struct {
	rt.Suite
}

func init() {
	rt.Register(&SessionOwner{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Sudo),
		rt.On("linux"),
	)
}

// connectHost makes a raw host-mode connect to the suite's namespace.
func (s *SessionOwner) connectHost() {
	_, stderr, err := s.CLI().Run(s.Ctx(), "connect",
		"--namespace", s.AppNamespace(),
		"--manager-namespace", managers.ManagerNamespace,
		"--as", connectAs)
	s.Require().NoError(err, "connect: %s", stderr)
}

// claimConnection frees the shared connection fixture and registers the
// cleanup that stops the daemons and drops the memoized connections.
func (s *SessionOwner) claimConnection() {
	t := s.T()
	freeDefaultConnection(t, s.AppNamespace())
	t.Cleanup(func() {
		if _, _, err := s.R().CLI().Run(context.WithoutCancel(s.Ctx()), "quit", "-s"); err != nil {
			s.R().Infof("[rtest] SessionOwner: quit -s: %v", err)
		}
		s.R().ForgetConnections()
	})
}

// daemonPort reads the daemon port from the info file under dir ("userd" or
// "rootd") in the cache directory. It returns 0 if there is no such file.
func (s *SessionOwner) daemonPort(dir string) int {
	data, err := os.ReadFile(filepath.Join(s.R().UserCacheDir(), dir, daemon.InfoFileName))
	if err != nil {
		return 0
	}
	var info daemon.Info
	if err = json.Unmarshal(data, &info); err != nil {
		return 0
	}
	return int(info.DaemonPort)
}

var ssPid = regexp.MustCompile(`pid=(\d+)`)

// listenerPid returns the pid of the process that listens on the loopback
// TCP port, which is the user daemon's gRPC port. Only that one socket is
// looked at, so no unrelated process can be picked.
func listenerPid(ctx context.Context, port int) (int, error) {
	out, err := exec.CommandContext(ctx, "ss", "-H", "-l", "-t", "-n", "-p", "sport", "=", ":"+strconv.Itoa(port)).Output()
	if err != nil {
		return 0, fmt.Errorf("ss: %w", err)
	}
	m := ssPid.FindSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("no listener with a visible pid on port %d: %q", port, out)
	}
	return strconv.Atoi(string(m[1]))
}

// portOpen reports whether something accepts TCP connections on the loopback port.
func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// reachable reports whether a GET of url answers 200.
func reachable(url string) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// waitFor polls cond until it holds, and fails the test with msg after networkDownTimeout.
func waitFor(t testing.TB, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(networkDownTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s (waited %s)", msg, networkDownTimeout)
		}
		time.Sleep(pollInterval)
	}
}

// Test_KilledUserDaemonFreesRootDaemon kills the user daemon with SIGKILL and
// expects the root daemon to take the network down, so that a new connect
// works without `quit -s`.
func (s *SessionOwner) Test_KilledUserDaemonFreesRootDaemon() {
	t := s.T()
	wl := s.Workload(workloads.Echo("owner-kill"))
	s.claimConnection()
	s.connectHost()
	rt.RoutedToCluster(t, wl.ServiceURL())

	// The info file names the user daemon's port, and the process that
	// listens on it is the user daemon.
	port := s.daemonPort("userd")
	s.Require().NotZero(port, "the info file must name the user daemon's port")
	pid, err := listenerPid(s.Ctx(), port)
	s.Require().NoError(err)
	proc, err := os.FindProcess(pid)
	s.Require().NoError(err)
	s.Require().NoError(proc.Kill(), "kill user daemon (pid %d)", pid)

	waitFor(t, func() bool { return !reachable(wl.ServiceURL()) },
		"the service is still reachable after the user daemon was killed")

	// No quit -s: the root daemon must accept a new user daemon. The killed
	// daemon's info file counts as alive until it has gone unrefreshed for a
	// few seconds, and connect fails with "not running" until then.
	deadline := time.Now().Add(staleInfoTimeout)
	for {
		_, stderr, err := s.CLI().Run(s.Ctx(), "connect",
			"--namespace", s.AppNamespace(),
			"--manager-namespace", managers.ManagerNamespace,
			"--as", connectAs)
		if err == nil {
			break
		}
		s.Require().Contains(stderr, "user daemon is not running", "connect: %s", stderr)
		s.Require().True(time.Now().Before(deadline), "connect still fails after %s: %s", staleInfoTimeout, stderr)
		time.Sleep(pollInterval)
	}
	rt.RoutedToCluster(t, wl.ServiceURL())
}

// Test_QuitStopsBothDaemons expects `quit -s` to leave neither daemon running.
func (s *SessionOwner) Test_QuitStopsBothDaemons() {
	t := s.T()
	ctx := s.Ctx()
	s.claimConnection()
	s.connectHost()

	userPort := s.daemonPort("userd")
	s.Require().NotZero(userPort, "the info file must name the user daemon's port")
	rootPort := s.daemonPort("rootd")

	stdout, stderr, err := s.CLI().Run(ctx, "status", "--format", "json")
	s.Require().NoError(err, "status: %s", stderr)
	var st struct {
		RootDaemon struct {
			Running bool `json:"running"`
			Managed bool `json:"managed"`
		} `json:"root_daemon"`
	}
	s.Require().NoError(json.Unmarshal([]byte(stdout), &st))
	s.Require().True(st.RootDaemon.Running, "root daemon must be running after connect")
	managed := st.RootDaemon.Managed
	if managed {
		t.Log("the root daemon is a managed service; only the user daemon is checked")
	} else {
		s.Require().NotZero(rootPort, "the info file must name the root daemon's port")
	}

	_, stderr, err = s.CLI().Run(ctx, "quit", "-s")
	s.Require().NoError(err, "quit -s: %s", stderr)

	waitFor(t, func() bool { return !portOpen(userPort) },
		"the user daemon still accepts connections after quit -s")
	if !managed {
		waitFor(t, func() bool { return !portOpen(rootPort) },
			"the root daemon still accepts connections after quit -s")
	}
	waitFor(t, func() bool {
		out, _, err := s.CLI().Run(ctx, "status", "--format", "json")
		if err != nil {
			return false
		}
		var after struct {
			RootDaemon struct {
				Running bool `json:"running"`
			} `json:"root_daemon"`
			UserDaemon struct {
				Running bool `json:"running"`
			} `json:"user_daemon"`
		}
		return json.Unmarshal([]byte(out), &after) == nil && !after.UserDaemon.Running && (managed || !after.RootDaemon.Running)
	}, "status still reports a running daemon after quit -s")
}

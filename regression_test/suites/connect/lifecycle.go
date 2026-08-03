package connect

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// connectAs reconstructs rt's private connectAs identity from exported
// pieces: the --as value the manager's clientRbac ClusterRoleBinding grants
// access to. Needed here because Test_Lifecycle's second `connect` call is a
// raw CLI invocation (proving reconnection under a fresh session, not
// something a Conn/ConnectionFixture round trip would exercise).
const connectAs = "system:serviceaccount:" + managers.ManagerNamespace + ":" + managers.TestServiceAccount

// listContains reports whether entries contains a workload named name in ns.
func listContains(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}

// randSuffix returns a short, unique-enough suffix for throwaway namespace
// names and iptables comments within a single run.
func randSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

// freeDefaultConnection claims the shared default connection fixture (so the
// engine re-provisions it fresh for later areas, per the connect area's
// contract) and quits whatever daemon is currently running under that
// identity. Tests that are about to connect under a different identity (a
// different namespace, or the same namespace with a different derived
// kubeconfig) must call this first: the host (non-docker) connector daemon
// is a singleton, keyed only by (kube context, namespace, name) and sharing
// one info file regardless of name (pkg/client/cli/daemon.Identifier.
// InfoFileName), so a second `connect` against a differently-configured
// cluster view would either collide with, or silently reuse the session of,
// whatever is already running there instead of applying the new settings.
func freeDefaultConnection(t *testing.T, ns string) {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)
}

// ConnectLifecycle proves the basic connect -> status -> disconnect ->
// connect -> quit cycle against the shared default connection. Carries
// CompatCore: Test_Lifecycle exercises WatchClusterInfo (rootd's persistent
// watch) and ReconnectClient (the live session's reconnect path); see
// framework/compat/manifest.go.
type ConnectLifecycle struct {
	rt.Suite
}

func init() {
	rt.Register(&ConnectLifecycle{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
		rt.WithLabels(rt.CompatCore),
	)
}

// Test_Lifecycle drives connect -> status -> disconnect (session only,
// daemons stay up) -> connect again -> quit (stop daemons) against the
// default connection. It acquires the connection via Mutate rather than
// Connect because it deliberately disturbs it (including a full stop at the
// end): later areas must see the engine re-provision a fresh one.
func (s *ConnectLifecycle) Test_Lifecycle() {
	t := s.T()
	ns := s.AppNamespace()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))

	st := conn.Status(t)
	s.True(st.UserDaemon.Running, "user daemon should be running after connect")
	s.True(st.RootDaemon.Running, "root daemon should be running after connect")
	s.Equal(ns, st.UserDaemon.Namespace)

	// Disconnect: session only, the daemons stay up.
	_, stderr, err := s.CLI().Run(s.Ctx(), "quit")
	s.Require().NoError(err, "quit: %s", stderr)

	// Connect again: a fresh session on the same daemons.
	_, stderr, err = s.CLI().Run(s.Ctx(), "connect",
		"--namespace", ns,
		"--manager-namespace", managers.ManagerNamespace,
		"--as", connectAs)
	s.Require().NoError(err, "connect: %s", stderr)

	st = conn.Status(t)
	s.True(st.UserDaemon.Running, "user daemon should be running after reconnect")
	s.Equal(ns, st.UserDaemon.Namespace)

	// Quit: stop all daemons.
	conn.Disconnect(t)
}

// apiServerDropDuration is how long the API server stays unreachable.
// recoveryTimeout bounds how long the session is given to recover once
// connectivity is restored; recoveryPollInterval is the poll interval used
// while waiting.
const (
	apiServerDropDuration = 7 * time.Second
	recoveryTimeout       = 60 * time.Second
	recoveryPollInterval  = 2 * time.Second
)

// ConnectReconnect proves that a session recovers on its own after the
// Kubernetes API server becomes briefly unreachable, without a manual
// reconnect. Gated on passwordless sudo (iptables) and Linux.
type ConnectReconnect struct {
	rt.Suite
}

func init() {
	rt.Register(&ConnectReconnect{},
		rt.InArea("connect"),
		rt.NeedsManager(managers.Default),
		rt.Requires(rt.Sudo),
		rt.On("linux"),
	)
}

// Test_ReconnectAfterApiServerDrop drops tcp traffic to the Kubernetes API
// server for a while, restores it, and asserts an active intercept's
// session recovers on its own. It targets the API server address directly
// through a single tagged OUTPUT rule instead of a redirect chain, so
// cleanup can delete precisely by comment and always runs, even on failure.
func (s *ConnectReconnect) Test_ReconnectAfterApiServerDrop() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))

	wl := s.Workload(workloads.Echo("reconnect-echo"))
	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)

	host, port, err := apiServerHostPort(ctx, s.R())
	s.Require().NoError(err)

	comment := "rtest-reconnect-" + randSuffix()
	addArgs := []string{
		"-t", "filter", "-A", "OUTPUT", "-p", "tcp",
		"-d", host, "--dport", port, "-j", "DROP",
		"-m", "comment", "--comment", comment,
	}
	delArgs := append([]string(nil), addArgs...)
	delArgs[2] = "-D" // same rule spec, -A swapped for -D deletes it precisely

	s.Require().NoError(sudoIPTables(ctx, addArgs...))
	removed := false
	removeDrop := func() {
		if removed {
			return
		}
		removed = true
		if err := sudoIPTables(ctx, delArgs...); err != nil {
			s.R().Infof("[rtest] reconnect: removing iptables DROP rule: %v", err)
		}
	}
	defer removeDrop()

	time.Sleep(apiServerDropDuration)
	removeDrop()

	// The session should recover without a manual reconnect. A bare `list`
	// must never run while the daemon might still be gone: unlike status,
	// list implicitly connects (to namespace "default") when no daemon is
	// running, which pollutes state (observed live). So poll status first,
	// and only call list once it confirms a live, connected daemon.
	waitForStatusRecovery(t, s.CLI(), ctx)
	s.True(listContains(conn.List(t), wl.Name, wl.Namespace),
		"list should show %s.%s after recovery", wl.Name, wl.Namespace)
	rt.RoutedToLocal(t, wl.ServiceURL(), ls)
	// No Disconnect here: the deferred Detach must run against the live
	// daemon, and rt.Mutate already makes the next user re-provision.
}

// waitForStatusRecovery polls `telepresence status --format json` until the
// user daemon reports running and connected to a traffic manager, up to
// recoveryTimeout, sleeping recoveryPollInterval between attempts.
func waitForStatusRecovery(t testing.TB, tp *cli.TP, ctx context.Context) {
	t.Helper()
	deadline := time.Now().Add(recoveryTimeout)
	for {
		stdout, stderr, err := tp.Run(ctx, "status", "--format", "json")
		if err == nil {
			var st cli.Status
			if uerr := json.Unmarshal([]byte(stdout), &st); uerr == nil &&
				st.UserDaemon.Running && st.TrafficManager.Name != "" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not survive the API-server drop: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		time.Sleep(recoveryPollInterval)
	}
}

// apiServerHostPort returns the Kubernetes API server's host (resolved to an
// IP address) and port, parsed from the run's kubeconfig.
func apiServerHostPort(ctx context.Context, r *rt.Runtime) (host, port string, err error) {
	out, err := r.Kubectl(ctx, "", "config", "view", "--minify", "--raw",
		"-o", "jsonpath={.clusters[0].cluster.server}")
	if err != nil {
		return "", "", fmt.Errorf("kubectl config view: %w", err)
	}
	u, err := url.Parse(strings.TrimSpace(out))
	if err != nil {
		return "", "", fmt.Errorf("parsing API server URL %q: %w", out, err)
	}
	host = u.Hostname()
	port = u.Port()
	if port == "" {
		port = "443"
	}
	if net.ParseIP(host) != nil {
		return host, port, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return "", "", fmt.Errorf("resolving API server host %q: %w", host, err)
	}
	return ips[0].String(), port, nil
}

// sudoIPTables runs `sudo iptables <args...>`, folding stderr/stdout into
// the returned error on failure.
func sudoIPTables(ctx context.Context, args ...string) error {
	full := append([]string{"iptables"}, args...)
	out, err := exec.CommandContext(ctx, "sudo", full...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sudo iptables %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return nil
}

package namespaces

import (
	"context"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// connectAs reconstructs rt's private connectAs identity (rt/fixture_connection.go)
// from exported pieces: the --as value the manager's clientRbac
// ClusterRoleBinding grants access to. Needed here because this package
// drives some connect attempts through raw CLI invocations rather than
// rt.ConnOpt, see mapped_namespaces.go's doc comment for why.
const connectAs = "system:serviceaccount:" + managers.ManagerNamespace + ":" + managers.TestServiceAccount

// labelPollInterval/labelPollTimeout bound how long a namespace label change
// takes to reach the manager's namespaceSelector watcher.
// attachPollInterval/attachPollTimeout bound how long an intercept takes to
// show up in `list`.
const (
	labelPollInterval  = 2 * time.Second
	labelPollTimeout   = 60 * time.Second
	attachPollInterval = time.Second
	attachPollTimeout  = 30 * time.Second
)

// freeDefaultConnection claims the shared default connection fixture (so the
// engine re-provisions it fresh afterwards) and quits whatever daemon is
// running under that identity. The host (non-docker) connector daemon is a
// singleton keyed only by (kube context, namespace, name), so a test about
// to connect under a different namespace via raw CLI must call this first:
// mirrors suites/connect/lifecycle.go's identically named helper (private to
// that package, so duplicated here rather than imported).
//
// Callers must ensure ns is currently manager-managed at the time of the
// call: this Mutates (and may therefore reprovision) the ConnectionFixture
// for ns, which fails the test if ns isn't reachable. In particular, a test
// that also switches the shared release's spec must free the default
// connection BEFORE making that switch, while the previous spec (which the
// suite is presumed to have already ensured, e.g. via Suite.Manager) still
// manages ns.
func freeDefaultConnection(t *testing.T, ns string) {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)
}

// rawConnectArgs is the argument list for a raw `connect` invocation to ns
// against the shared manager, bypassing ConnectionFixture.
func rawConnectArgs(ns string) []string {
	return []string{"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace, "--as", connectAs}
}

// probeConnect attempts a raw connect to ns, always starting from a clean
// daemon slot (quit -s first) so the attempt reflects the manager's current
// admission decision rather than an earlier, possibly stale session. On
// success the daemon is left connected, for a caller polling toward "should
// succeed" to build on (typically handed to rt.ConnectionFixture next, to
// adopt it as a proper *rt.Conn); on failure nothing is left running.
func probeConnect(t *testing.T, r *rt.Runtime, ctx context.Context, ns string) (ok bool, stderr string) {
	t.Helper()
	if _, _, err := r.CLI().Run(ctx, "quit", "-s"); err != nil {
		r.Infof("[rtest] namespaces: quit -s before connect probe to %s: %v", ns, err)
	}
	_, stderr, err := r.CLI().Run(ctx, rawConnectArgs(ns)...)
	return err == nil, stderr
}

// quitDefensively is registered as a t.Cleanup by every test in this package
// that probes connect admission via raw CLI: a safety net in case a probe's
// own bookkeeping left a daemon running, so the next test never adopts a
// stale or misconfigured session.
func quitDefensively(t *testing.T, r *rt.Runtime, ctx context.Context, who string) {
	t.Helper()
	t.Cleanup(func() {
		if _, _, err := r.CLI().Run(ctx, "quit", "-s"); err != nil {
			r.Infof("[rtest] %s: quit -s: %v", who, err)
		}
	})
}

// present reports whether entries contains a workload named name in ns.
func present(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return true
		}
	}
	return false
}

// attached reports whether the workload named name in ns currently carries
// an intercept or ingest.
func attached(entries []cli.ListEntry, name, ns string) bool {
	for _, e := range entries {
		if e.Name == name && e.Namespace == ns {
			return len(e.InterceptInfo)+len(e.IngestInfo) > 0
		}
	}
	return false
}

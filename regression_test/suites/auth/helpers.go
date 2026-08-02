package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// switchManagerSpec frees whatever connection to ns is currently memoized
// while the release still runs its previous spec, switches the shared
// release to spec via rt.Mutate, and reconnects to ns, returning the live
// *rt.Conn. Mirrors suites/injector/helpers.go's identically named helper
// (private to that package, duplicated here rather than imported).
func switchManagerSpec(t *testing.T, ctx context.Context, spec managers.Spec, ns string) *rt.Conn {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)
	rt.Mutate(t, rt.ManagerFixture(spec))
	return rt.Reconnect(t, ctx, ns)
}

// freeDefaultConnection claims the shared default connection fixture (so the
// engine re-provisions it fresh afterwards) and quits whatever daemon is
// running under that identity. Mirrors suites/namespaces/helpers.go's
// identically named helper; needed here because this package also drives
// raw CLI connect attempts under identities other than the framework's
// default connectAs.
func freeDefaultConnection(t *testing.T, ns string) {
	t.Helper()
	conn := rt.Mutate(t, rt.ConnectionFixture(ns))
	conn.Disconnect(t)
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
		r.ForgetConnections()
	})
}

// identity returns the --as value for a ServiceAccount named name in the
// manager namespace.
func identity(name string) string {
	return "system:serviceaccount:" + managers.ManagerNamespace + ":" + name
}

// rawConnectArgsAs is the argument list for a raw `connect` invocation to ns
// against the shared manager, using asIdentity for --as instead of the
// framework's default connectAs.
func rawConnectArgsAs(ns, asIdentity string) []string {
	return []string{"connect", "--namespace", ns, "--manager-namespace", managers.ManagerNamespace, "--as", asIdentity}
}

// probeConnectAs attempts a raw connect to ns as asIdentity, always starting
// from a clean daemon slot (quit -s first) so the attempt reflects the
// cluster's current admission decision rather than an earlier, possibly
// stale session. On success the daemon is left connected; on failure nothing
// is left running. Mirrors suites/namespaces/helpers.go's probeConnect,
// parameterized on the --as identity.
func probeConnectAs(t *testing.T, r *rt.Runtime, ctx context.Context, ns, asIdentity string) (ok bool, stderr string) {
	t.Helper()
	if _, _, err := r.CLI().Run(ctx, "quit", "-s"); err != nil {
		r.Infof("[rtest] auth: quit -s before connect probe to %s as %s: %v", ns, asIdentity, err)
	}
	r.ForgetConnections()
	_, stderr, err := r.CLI().Run(ctx, rawConnectArgsAs(ns, asIdentity)...)
	return err == nil, stderr
}

// createNoGrantsServiceAccount creates a ServiceAccount named name in the
// manager namespace with no RoleBinding beyond the chart's own: none of
// clientRbac's Roles reference an arbitrary name, so it carries no RBAC
// permissions at all -- not even the traffic-manager-connect Role that
// grants pods/portforward create. Deleted before creation (idempotent
// against a stale leftover from an interrupted earlier run) and cleaned up
// via t.Cleanup.
func createNoGrantsServiceAccount(t *testing.T, ctx context.Context, r *rt.Runtime, name string) string {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	if _, err := r.Kubectl(ctx, mgrNS, "delete", "serviceaccount", name, "--ignore-not-found"); err != nil {
		t.Fatalf("deleting stale ServiceAccount %s: %v", name, err)
	}
	if _, err := r.Kubectl(ctx, mgrNS, "create", "serviceaccount", name); err != nil {
		t.Fatalf("creating ServiceAccount %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := r.Kubectl(ctx, mgrNS, "delete", "serviceaccount", name, "--ignore-not-found"); err != nil {
			r.Infof("[rtest] auth: deleting ServiceAccount %s: %v", name, err)
		}
	})
	return name
}

// kubectlCreateToken runs `kubectl create token <name>` in ns and returns the
// trimmed bearer token.
func kubectlCreateToken(t *testing.T, ctx context.Context, r *rt.Runtime, ns, name string) string {
	t.Helper()
	out, err := r.Kubectl(ctx, ns, "create", "token", name)
	if err != nil {
		t.Fatalf("creating token for %s: %v", name, err)
	}
	return strings.TrimSpace(out)
}

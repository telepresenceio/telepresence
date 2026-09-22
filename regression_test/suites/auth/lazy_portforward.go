package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// lazyPortForwardRouteTimeout bounds routedThroughManager. A name in a
// portforward-denied namespace resolves through the traffic-manager's Lookup
// RPC (no direct agent connection exists to answer it): the first attempt
// commonly misses its own deadline and is cached as a miss for up to 60s
// (pkg/client/rootd/dns/server.go's cacheTTL) before DNS retries, and each
// probe then re-opens a fresh manager tunnel (routedThroughManager disables
// keep-alives, like check.EventuallyHTTP), which costs more than a direct
// connection would.
const lazyPortForwardRouteTimeout = 150 * time.Second

// routedThroughManagerProbeTimeout is the per-request budget routedThroughManager
// gives each probe: generous enough for a DNS lookup relayed through the
// traffic-manager plus a manager-tunneled HTTP round trip, both slower than
// check.EventuallyHTTP's 1s budget (tuned for a direct connection) allows.
const routedThroughManagerProbeTimeout = 10 * time.Second

// routedThroughManagerPollInterval is the delay between probes.
const routedThroughManagerPollInterval = 2 * time.Second

// routedThroughManager polls url until its body contains marker, or fails t
// once timeout elapses. It is check.EventuallyHTTP's shape, but with a
// per-request timeout long enough for a request relayed through the
// traffic-manager rather than dialed directly to the agent.
func routedThroughManager(t testing.TB, url, marker string, timeout time.Duration) {
	t.Helper()
	client := &http.Client{
		Timeout:   routedThroughManagerProbeTimeout,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		resp, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil && strings.Contains(string(body), marker) {
				return
			}
			lastErr = fmt.Errorf("unexpected body %q (read err %v)", body, readErr)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("routedThroughManager %s: timed out after %s: %v", url, timeout, lastErr)
		}
		time.Sleep(routedThroughManagerPollInterval)
	}
}

// connectOnlyRules grants exactly what the chart's traffic-manager-connect
// Role grants without clientRbac.legacyAccess: pods/portforward create
// scoped to the known traffic-manager pod name, plus
// connections.telepresence.io create. No attachments grant here -- that is
// bound in the app namespace instead, by appAttachOnlyManifest.
const connectOnlyRules = `  - apiGroups: [""]
    resources: ["pods/portforward"]
    resourceNames: ["traffic-manager-0"]
    verbs: ["create"]
  - apiGroups: ["telepresence.io"]
    resources: ["connections"]
    verbs: ["create"]`

// appAttachOnlyManifest is grantIdentityManifest's app-namespace
// counterpart: a Role granting attachments.telepresence.io create/get in
// the app namespace (%[2]s), bound to the ServiceAccount named %[1]s that
// lives in the manager namespace (%[3]s). The pods get/list grant is the
// same legacy discovery rule clientRbacInterceptRules always renders
// (namespace-accessibility completion); it says nothing about reaching an
// agent pod. No pods/portforward grant at all, so a port-forward dial to
// an agent pod in this namespace is refused.
const appAttachOnlyManifest = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %[1]s
  namespace: %[2]s
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: ["telepresence.io"]
    resources: ["attachments"]
    verbs: ["create", "get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %[1]s
  namespace: %[2]s
subjects:
  - kind: ServiceAccount
    name: %[1]s
    namespace: %[3]s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %[1]s
`

// createAppAttachRole applies appAttachOnlyManifest for name in appNS,
// deleting any stale leftover first, and registers t.Cleanup to remove it.
func createAppAttachRole(t *testing.T, ctx context.Context, r *rt.Runtime, name, appNS string) {
	t.Helper()
	deleteAppAttachRole(t, ctx, r, name, appNS)

	manifest := fmt.Sprintf(appAttachOnlyManifest, name, appNS, managers.ManagerNamespace)
	path := filepath.Join(t.TempDir(), name+"-app.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing app-namespace RBAC manifest for %s: %v", name, err)
	}
	if _, err := r.Kubectl(ctx, appNS, "apply", "-f", path); err != nil {
		t.Fatalf("applying app-namespace RBAC manifest for %s: %v", name, err)
	}
	t.Cleanup(func() { deleteAppAttachRole(t, ctx, r, name, appNS) })
}

// deleteAppAttachRole removes the RoleBinding and Role createAppAttachRole
// creates, ignoring a missing resource.
func deleteAppAttachRole(t *testing.T, ctx context.Context, r *rt.Runtime, name, appNS string) {
	t.Helper()
	for _, kind := range []string{"rolebinding", "role"} {
		if _, err := r.Kubectl(ctx, appNS, "delete", kind, name, "--ignore-not-found"); err != nil {
			t.Fatalf("deleting %s %s in %s: %v", kind, name, appNS, err)
		}
	}
}

// portForwardRefusalLogged reports whether the root daemon's log recorded
// the pods/portforward refusal for ns: daemon.log, or any daemon-*.log this
// run rotated.
func portForwardRefusalLogged(t *testing.T, r *rt.Runtime, ns string) bool {
	t.Helper()
	needle := fmt.Sprintf("direct agent access in namespace %s refused (pods/portforward)", ns)
	paths, err := filepath.Glob(filepath.Join(r.LogDir(), "daemon*.log"))
	if err != nil {
		t.Fatalf("globbing daemon logs: %v", err)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), needle) {
			return true
		}
	}
	return false
}

// AttachWithoutPortForward proves the lazy port-forward fallback
// (pkg/client/agentpf/portforward_deny.go): an identity that can connect
// and attach (connections/attachments.telepresence.io) but holds no
// pods/portforward in the target namespace still intercepts successfully,
// with agent traffic routed through the traffic-manager and the refusal
// logged once per namespace.
type AttachWithoutPortForward struct {
	rt.Suite
}

func init() {
	rt.Register(&AttachWithoutPortForward{}, rt.InArea("auth"), rt.NeedsManager(authGrantSpec("telepresence")))
}

// Test_InterceptWithoutPodsPortForward creates an identity that can connect
// and attach but has no pods/portforward in the app namespace, connects and
// intercepts as that identity, and asserts both that the intercept works
// (traffic reaches the local handler through the manager) and that the root
// daemon logged the namespace as refused for direct agent access.
func (s *AttachWithoutPortForward) Test_InterceptWithoutPodsPortForward() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGrantIdentity(t, ctx, r, "rtest-auth-lazy-pf", connectOnlyRules)
	createAppAttachRole(t, ctx, r, name, ns)

	freeDefaultConnection(t, ns)
	quitDefensively(t, r, ctx, "AttachWithoutPortForward:Test_InterceptWithoutPodsPortForward")

	// --mapped-namespaces ns keeps the client from watching every namespace
	// in the (shared, long-lived) cluster: with the default "all namespaces"
	// mode, the client-side accessibility scan (one SelfSubjectAccessReview
	// per cluster namespace) can take far longer than this test's routing
	// timeout on a cluster that has accumulated many namespaces.
	conn := rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnExtraArgs("--as", identity(name), "--mapped-namespaces", ns)))

	wl := s.Workload(workloads.Echo("lazy-pf"))
	ls := s.LocalEcho()
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse())
	defer a.Detach(t)
	routedThroughManager(t, wl.ServiceURL(), ls.Marker(), lazyPortForwardRouteTimeout)

	s.Eventually(func() bool {
		return portForwardRefusalLogged(t, r, ns)
	}, 30*time.Second, 500*time.Millisecond,
		"expected the root daemon log to record the pods/portforward refusal for namespace %s", ns)
}

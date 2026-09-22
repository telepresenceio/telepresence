package auth

import (
	"context"
	"fmt"
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

// interceptFailTimeout bounds how long a doomed intercept attempt (no
// direct agent path and no QUIC tunnel) may take to fail: well under the
// intercept timeout it would otherwise wait out.
const interceptFailTimeout = 60 * time.Second

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
// lives in the manager namespace (%[3]s). No pods/portforward grant at
// all, so a port-forward dial to an agent pod in this namespace is
// refused.
const appAttachOnlyManifest = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %[1]s
  namespace: %[2]s
rules:
  # The client's namespace-accessibility probe needs pods get/list; it
  # says nothing about reaching an agent pod.
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

// AttachWithoutPortForward proves the fail-fast path
// (pkg/client/agentpf/clients.go's WaitForIP, pkg/client/userd/trafficmgr/
// podaccess.go's ensureAccess): an identity that can connect and attach
// (connections/attachments.telepresence.io) but holds no pods/portforward
// in the target namespace, and has no QUIC tunnel available either, fails
// its intercept quickly with a clear error instead of hanging, and the
// refusal is logged once for that namespace.
type AttachWithoutPortForward struct {
	rt.Suite
}

func init() {
	rt.Register(&AttachWithoutPortForward{}, rt.InArea("auth"), rt.NeedsManager(authGrantSpec("telepresence")))
}

// Test_InterceptWithoutPodsPortForward creates an identity that can connect
// and attach but has no pods/portforward in the app namespace, connects as
// that identity, and asserts that an intercept attempt fails quickly with
// an error naming both the missing pods/portforward grant and the QUIC
// tunnel, and that the root daemon logged the namespace as refused for
// direct agent access.
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
	// per cluster namespace) can take far longer than this test's fail-fast
	// budget on a cluster that has accumulated many namespaces.
	rt.Mutate(t, rt.ConnectionFixture(ns, rt.ConnExtraArgs("--as", identity(name), "--mapped-namespaces", ns)))

	wl := s.Workload(workloads.Echo("lazy-pf"))
	ls := s.LocalEcho()

	interceptOpts := append(rt.ToLocal(ls, "http")(), cli.MountFalse()()...)
	iArgs := append([]string{"intercept", wl.Name, "--namespace", ns}, interceptOpts...)

	start := time.Now()
	_, stderr, err := r.CLI().Run(ctx, iArgs...)
	elapsed := time.Since(start)

	s.Require().Error(err, "intercept should fail without a direct agent path or a QUIC tunnel")
	s.Contains(stderr, "pods/portforward")
	s.Contains(stderr, "QUIC tunnel")
	s.Less(elapsed, interceptFailTimeout, "intercept should fail quickly instead of waiting out a timeout")

	s.Eventually(func() bool {
		return portForwardRefusalLogged(t, r, ns)
	}, 30*time.Second, 500*time.Millisecond,
		"expected the root daemon log to record the pods/portforward refusal for namespace %s", ns)
}

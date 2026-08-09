package auth

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

// authGateSpec returns managers.AuthEnforcing() with
// security.authorization.gate layered on top to gate ("telepresence" or
// "any"), built inline (mirrors suites/quic/fallback.go's
// quicUnreachableSpec) since no other suite needs a non-default gate value.
func authGateSpec(gate string) managers.Spec {
	v := managers.AuthEnforcing().Values
	v.Security.Authorization.Gate = gate
	return managers.Spec{Key: "auth-gate/" + gate, Values: v}
}

// gateIdentityManifest creates a ServiceAccount, a Role granting exactly
// %[3]s (an indented rules list, no leading "rules:" key), and a
// RoleBinding wiring the two together, all named %[1]s in namespace %[2]s.
// Applying it grants name exactly those RBAC rules -- in particular, none
// of clientRbac's own Roles, which reference managers.TestServiceAccount
// only.
const gateIdentityManifest = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: %[1]s
  namespace: %[2]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %[1]s
  namespace: %[2]s
rules:
%[3]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %[1]s
  namespace: %[2]s
subjects:
  - kind: ServiceAccount
    name: %[1]s
    namespace: %[2]s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %[1]s
`

// portForwardOnlyRules grants exactly pods/portforward create -- the
// mechanical transport grant, and (under gate=portforward) the legacy
// authorization proxy for it -- with no telepresence.io attribute at all.
const portForwardOnlyRules = `  - apiGroups: [""]
    resources: ["pods/portforward"]
    verbs: ["create"]`

// telepresenceGrantRules grants create on connections.telepresence.io (what
// authorizeConnect reviews under gate=telepresence) and create/get on
// kind-qualified attachments.telepresence.io (what authorizeAttachment
// reviews for an intercept or ingest -- a bare "attachments" rule would
// never match those subresource reviews), with no pods/portforward at all:
// the RBAC shape a minimal-RBAC client is meant to hold once the mechanics
// move manager-side.
const telepresenceGrantRules = `  - apiGroups: ["telepresence.io"]
    resources: ["connections"]
    verbs: ["create"]
  - apiGroups: ["telepresence.io"]
    resources: ["attachments/deployment"]
    verbs: ["create", "get"]`

// telepresenceGrantWithLogsRules extends telepresenceGrantRules with get on
// logs.telepresence.io -- what CanGetLogs reviews for StreamLogs, a
// diagnostic attribute reviewed the same way regardless of Gate.
const telepresenceGrantWithLogsRules = telepresenceGrantRules + `
  - apiGroups: ["telepresence.io"]
    resources: ["logs"]
    verbs: ["get"]`

// createGateIdentity applies gateIdentityManifest for name with rulesYAML,
// after deleting any stale leftover of the same name (idempotent against an
// interrupted earlier run), and registers t.Cleanup to remove it. Mirrors
// createNoGrantsServiceAccount, parameterized on the granted rules.
func createGateIdentity(t *testing.T, ctx context.Context, r *rt.Runtime, name, rulesYAML string) string {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	deleteGateIdentity(t, ctx, r, name)

	manifest := fmt.Sprintf(gateIdentityManifest, name, mgrNS, rulesYAML)
	path := filepath.Join(t.TempDir(), name+".yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing RBAC manifest for %s: %v", name, err)
	}
	if _, err := r.Kubectl(ctx, mgrNS, "apply", "-f", path); err != nil {
		t.Fatalf("applying RBAC manifest for %s: %v", name, err)
	}
	t.Cleanup(func() { deleteGateIdentity(t, ctx, r, name) })
	return name
}

// deleteGateIdentity removes the RoleBinding, Role, and ServiceAccount
// createGateIdentity creates, ignoring a missing resource.
func deleteGateIdentity(t *testing.T, ctx context.Context, r *rt.Runtime, name string) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	for _, kind := range []string{"rolebinding", "role", "serviceaccount"} {
		if _, err := r.Kubectl(ctx, mgrNS, "delete", kind, name, "--ignore-not-found"); err != nil {
			t.Fatalf("deleting %s %s: %v", kind, name, err)
		}
	}
}

// arriveAsClient dials the shared manager directly (bypassing the CLI's own
// port-forward transport, which the run's operator credentials establish,
// not the identity under test) and calls ArriveAsClient with tok as the
// caller's bearer token, isolating the manager's own authorization decision
// from the Kubernetes API server's port-forward admission --
// AuthEnforcing.Test_UnauthorizedIdentityDeniedAtConnect already covers the
// latter. Registers a Depart cleanup on success; returns the error either
// way, since every caller here only asserts admission or denial.
func arriveAsClient(t *testing.T, ctx context.Context, r *rt.Runtime, ns, name, tok string) error {
	t.Helper()
	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	if err != nil {
		t.Fatalf("dialing manager: %v", err)
	}
	t.Cleanup(closeFn)

	tokCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	si, err := mc.ArriveAsClient(tokCtx, &manager.ClientInfo{
		Name:      name,
		Namespace: ns,
		InstallId: name,
		Product:   "telepresence",
		Version:   "v" + r.Version().String(),
	})
	if err == nil {
		t.Cleanup(func() { _, _ = mc.Depart(tokCtx, si) })
	}
	return err
}

// dialAndArrive is arriveAsClient for a caller that needs the resulting
// SessionInfo itself, to drive a further RPC on the same session (StreamLogs,
// WatchNamespaces). Fails the test outright on either the dial or the
// ArriveAsClient call, since every caller here expects admission to succeed.
// Registers cleanups for both the connection and the session.
func dialAndArrive(t *testing.T, ctx context.Context, r *rt.Runtime, ns, name, tok string) (manager.ManagerClient, context.Context, *manager.SessionInfo) {
	t.Helper()
	mc, closeFn, err := rt.ManagerClient(rt.Env{Ctx: ctx, T: t, R: r}, managers.ManagerNamespace)
	if err != nil {
		t.Fatalf("dialing manager: %v", err)
	}
	t.Cleanup(closeFn)

	tokCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	si, err := mc.ArriveAsClient(tokCtx, &manager.ClientInfo{
		Name:      name,
		Namespace: ns,
		InstallId: name,
		Product:   "telepresence",
		Version:   "v" + r.Version().String(),
	})
	if err != nil {
		t.Fatalf("ArriveAsClient for %s: %v", name, err)
	}
	t.Cleanup(func() { _, _ = mc.Depart(tokCtx, si) })
	return mc, tokCtx, si
}

// AuthGate proves security.authorization.gate at the session boundary: under
// gate=telepresence, an identity whose Role carries only pods/portforward is
// refused a SESSION -- not merely an intercept, the distinguishing negative
// phase 1 introduces -- while an identity whose Role carries the
// telepresence.io connect/attach grants is admitted; under gate=any, both
// grants admit a session.
//
// The manager namespace's transport-only pods/portforward Role
// (traffic-manager-connect, rendered for every gate value) has no bearing
// on either identity here: both dial the manager directly via
// rt.ManagerClient, never through a real port-forward, so what a
// PermissionDenied SubjectAccessReview would deny at the API server is not
// in scope for this suite -- see AuthEnforcing for that boundary.
type AuthGate struct {
	rt.Suite
}

func init() {
	rt.Register(&AuthGate{}, rt.InArea("auth"), rt.NeedsManager(authGateSpec("telepresence")))
}

// Test_PortForwardOnlyGrantRefusedSessionUnderTelepresenceGate covers the
// plan's negative case: an authenticated caller holding only
// pods/portforward is refused a session (PermissionDenied on ArriveAsClient
// itself), not merely denied later at intercept time.
func (s *AuthGate) Test_PortForwardOnlyGrantRefusedSessionUnderTelepresenceGate() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGateIdentity(t, ctx, r, "rtest-auth-gate-pf-only", portForwardOnlyRules)
	tok := kubectlCreateToken(t, ctx, r, name)

	err := arriveAsClient(t, ctx, r, ns, name, tok)
	s.Require().Error(err, "a pods/portforward-only identity must be refused a session under gate=telepresence")
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.PermissionDenied, st.Code())
}

// Test_TelepresenceGrantAdmittedUnderTelepresenceGate covers the matching
// positive: an identity holding create on connections.telepresence.io (plus
// the attachments grant an intercept or ingest would need) is admitted.
func (s *AuthGate) Test_TelepresenceGrantAdmittedUnderTelepresenceGate() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGateIdentity(t, ctx, r, "rtest-auth-gate-tp-grant", telepresenceGrantRules)
	tok := kubectlCreateToken(t, ctx, r, name)

	err := arriveAsClient(t, ctx, r, ns, name, tok)
	s.Require().NoError(err, "an identity holding create connections.telepresence.io should be admitted under gate=telepresence")
}

// Test_GateAnyAdmitsEitherGrant covers gate=any's accept-either contract:
// both the legacy pods/portforward-only identity and the telepresence.io
// grant identity are admitted against the same release.
func (s *AuthGate) Test_GateAnyAdmitsEitherGrant() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	rt.Mutate(t, rt.ManagerFixture(authGateSpec("any")))

	pfName := createGateIdentity(t, ctx, r, "rtest-auth-gate-any-pf", portForwardOnlyRules)
	pfTok := kubectlCreateToken(t, ctx, r, pfName)
	err := arriveAsClient(t, ctx, r, ns, pfName, pfTok)
	s.Require().NoError(err, "gate=any must admit the legacy pods/portforward grant too")

	tpName := createGateIdentity(t, ctx, r, "rtest-auth-gate-any-tp", telepresenceGrantRules)
	tpTok := kubectlCreateToken(t, ctx, r, tpName)
	err = arriveAsClient(t, ctx, r, ns, tpName, tpTok)
	s.Require().NoError(err, "gate=any must admit the telepresence.io grant")
}

// Test_StreamLogsDeniedNamespaceGetsErrorFrame covers StreamLogs's per-pod
// authorization behavior: an identity holding the connect/attach grants but
// no get on logs.telepresence.io is not refused the request outright. It
// gets a BEGIN frame, an error frame naming the denial, and an END frame for
// the traffic-manager pod, then the stream ends.
func (s *AuthGate) Test_StreamLogsDeniedNamespaceGetsErrorFrame() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGateIdentity(t, ctx, r, "rtest-auth-gate-logs-denied", telepresenceGrantRules)
	tok := kubectlCreateToken(t, ctx, r, name)
	mc, tokCtx, si := dialAndArrive(t, ctx, r, ns, name, tok)

	stream, err := mc.StreamLogs(tokCtx, &manager.StreamLogsRequest{Session: si, TrafficManager: true})
	s.Require().NoError(err)

	begin, err := stream.Recv()
	s.Require().NoError(err)
	s.Equal(manager.LogChunk_BEGIN, begin.GetFrame())
	s.Equal(managers.ManagerNamespace, begin.GetPodNamespace())

	errFrame, err := stream.Recv()
	s.Require().NoError(err)
	s.Contains(errFrame.GetError(), "not permitted to get logs.telepresence.io")

	end, err := stream.Recv()
	s.Require().NoError(err)
	s.Equal(manager.LogChunk_END, end.GetFrame())

	_, err = stream.Recv()
	s.Require().ErrorIs(err, io.EOF)
}

// Test_StreamLogsAuthorizedReceivesData covers the matching positive: an
// identity additionally holding get on logs.telepresence.io streams the
// traffic-manager pod's log with no denial frame, ending in an END frame.
func (s *AuthGate) Test_StreamLogsAuthorizedReceivesData() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGateIdentity(t, ctx, r, "rtest-auth-gate-logs-granted", telepresenceGrantWithLogsRules)
	tok := kubectlCreateToken(t, ctx, r, name)
	mc, tokCtx, si := dialAndArrive(t, ctx, r, ns, name, tok)

	stream, err := mc.StreamLogs(tokCtx, &manager.StreamLogsRequest{Session: si, TrafficManager: true})
	s.Require().NoError(err)

	begin, err := stream.Recv()
	s.Require().NoError(err)
	s.Equal(manager.LogChunk_BEGIN, begin.GetFrame())

	sawDataOrEnd := false
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		s.Require().NoError(err)
		s.Empty(chunk.GetError(), "an authorized caller should get no denial frame")
		if len(chunk.GetData()) > 0 || chunk.GetFrame() == manager.LogChunk_END {
			sawDataOrEnd = true
		}
	}
	s.True(sawDataOrEnd, "expected at least one data chunk or the END frame for the traffic-manager pod")
}

// Test_WatchNamespacesBasics covers the plumbing WatchNamespaces relies on: a
// bound session receives the current managed-namespace set on subscribe, and
// a session id the manager has never seen is refused outright.
func (s *AuthGate) Test_WatchNamespacesBasics() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGateIdentity(t, ctx, r, "rtest-auth-gate-watchns", telepresenceGrantRules)
	tok := kubectlCreateToken(t, ctx, r, name)
	mc, tokCtx, si := dialAndArrive(t, ctx, r, ns, name, tok)

	stream, err := mc.WatchNamespaces(tokCtx, si)
	s.Require().NoError(err)
	list, err := stream.Recv()
	s.Require().NoError(err)
	s.Contains(list.GetNamespaces(), ns, "the first NamespaceList should include the app namespace")

	garbage := &manager.SessionInfo{SessionId: "rtest-auth-gate-watchns-garbage-session"}
	gStream, err := mc.WatchNamespaces(tokCtx, garbage)
	s.Require().NoError(err)
	_, err = gStream.Recv()
	s.Require().Error(err, "WatchNamespaces with an unknown session id should be refused")
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.NotFound, st.Code())
}

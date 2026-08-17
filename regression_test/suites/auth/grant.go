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

// authGrantSpec returns managers.AuthEnforcing() with
// security.authorization.requiredGrant set to grant.
func authGrantSpec(grant string) managers.Spec {
	v := managers.AuthEnforcing().Values
	v.Security.Authorization.RequiredGrant = grant
	return managers.Spec{Key: "auth-grant/" + grant, Values: v}
}

// grantIdentityManifest creates a ServiceAccount, a Role granting exactly
// %[3]s, and a RoleBinding wiring them together, all named %[1]s in
// namespace %[2]s.
const grantIdentityManifest = `apiVersion: v1
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
// mechanical transport grant, and (when the required grant is portforward) the legacy
// authorization proxy for it -- with no telepresence.io attribute at all.
const portForwardOnlyRules = `  - apiGroups: [""]
    resources: ["pods/portforward"]
    verbs: ["create"]`

// telepresenceGrantRules grants create on connections.telepresence.io and
// create/get on attachments, with no pods/portforward at all.
const telepresenceGrantRules = `  - apiGroups: ["telepresence.io"]
    resources: ["connections"]
    verbs: ["create"]
  - apiGroups: ["telepresence.io"]
    resources: ["attachments"]
    verbs: ["create", "get"]`

// telepresenceGrantWithLogsRules extends telepresenceGrantRules with get on
// logs.telepresence.io.
const telepresenceGrantWithLogsRules = telepresenceGrantRules + `
  - apiGroups: ["telepresence.io"]
    resources: ["logs"]
    verbs: ["get"]`

// createGrantIdentity applies grantIdentityManifest for name with rulesYAML,
// deleting any stale leftover first, and registers t.Cleanup to remove it.
func createGrantIdentity(t *testing.T, ctx context.Context, r *rt.Runtime, name, rulesYAML string) string {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	deleteGrantIdentity(t, ctx, r, name)

	manifest := fmt.Sprintf(grantIdentityManifest, name, mgrNS, rulesYAML)
	path := filepath.Join(t.TempDir(), name+".yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing RBAC manifest for %s: %v", name, err)
	}
	if _, err := r.Kubectl(ctx, mgrNS, "apply", "-f", path); err != nil {
		t.Fatalf("applying RBAC manifest for %s: %v", name, err)
	}
	t.Cleanup(func() { deleteGrantIdentity(t, ctx, r, name) })
	return name
}

// deleteGrantIdentity removes the RoleBinding, Role, and ServiceAccount
// createGrantIdentity creates, ignoring a missing resource.
func deleteGrantIdentity(t *testing.T, ctx context.Context, r *rt.Runtime, name string) {
	t.Helper()
	mgrNS := managers.ManagerNamespace
	for _, kind := range []string{"rolebinding", "role", "serviceaccount"} {
		if _, err := r.Kubectl(ctx, mgrNS, "delete", kind, name, "--ignore-not-found"); err != nil {
			t.Fatalf("deleting %s %s: %v", kind, name, err)
		}
	}
}

// arriveAsClient dials the shared manager directly -- isolating the manager's
// own authorization decision from the API server's port-forward admission --
// and calls ArriveAsClient with tok as the bearer token. Registers a Depart
// cleanup on success; returns the error either way.
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
// SessionInfo to drive a further RPC on the same session. Fails the test
// outright if either call errors.
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

// AuthGrant proves security.authorization.requiredGrant at the session boundary: with
// requiredGrant=telepresence, a pods/portforward-only identity is refused a session
// while a telepresence.io connect/attach identity is admitted; under
// requiredGrant=any, both are admitted.
type AuthGrant struct {
	rt.Suite
}

func init() {
	rt.Register(&AuthGrant{}, rt.InArea("auth"), rt.NeedsManager(authGrantSpec("telepresence")))
}

// Test_PortForwardOnlyGrantRefusedSessionUnderTelepresenceGrant asserts that
// a caller holding only pods/portforward is refused a session with
// PermissionDenied.
func (s *AuthGrant) Test_PortForwardOnlyGrantRefusedSessionUnderTelepresenceGrant() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGrantIdentity(t, ctx, r, "rtest-auth-grant-pf-only", portForwardOnlyRules)
	tok := kubectlCreateToken(t, ctx, r, name)

	err := arriveAsClient(t, ctx, r, ns, name, tok)
	s.Require().Error(err, "a pods/portforward-only identity must be refused a session with requiredGrant=telepresence")
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.PermissionDenied, st.Code())
}

// Test_TelepresenceGrantAdmittedUnderTelepresenceGrant covers the matching
// positive: an identity holding create on connections.telepresence.io (plus
// the attachments grant an intercept or ingest would need) is admitted.
func (s *AuthGrant) Test_TelepresenceGrantAdmittedUnderTelepresenceGrant() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGrantIdentity(t, ctx, r, "rtest-auth-grant-tp-grant", telepresenceGrantRules)
	tok := kubectlCreateToken(t, ctx, r, name)

	err := arriveAsClient(t, ctx, r, ns, name, tok)
	s.Require().NoError(err, "an identity holding create connections.telepresence.io should be admitted with requiredGrant=telepresence")
}

// Test_GrantAnyAdmitsEitherGrant covers requiredGrant=any's accept-either contract:
// both the legacy pods/portforward-only identity and the telepresence.io
// grant identity are admitted against the same release.
func (s *AuthGrant) Test_GrantAnyAdmitsEitherGrant() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	rt.Mutate(t, rt.ManagerFixture(authGrantSpec("any")))

	pfName := createGrantIdentity(t, ctx, r, "rtest-auth-grant-any-pf", portForwardOnlyRules)
	pfTok := kubectlCreateToken(t, ctx, r, pfName)
	err := arriveAsClient(t, ctx, r, ns, pfName, pfTok)
	s.Require().NoError(err, "requiredGrant=any must admit the legacy pods/portforward grant too")

	tpName := createGrantIdentity(t, ctx, r, "rtest-auth-grant-any-tp", telepresenceGrantRules)
	tpTok := kubectlCreateToken(t, ctx, r, tpName)
	err = arriveAsClient(t, ctx, r, ns, tpName, tpTok)
	s.Require().NoError(err, "requiredGrant=any must admit the telepresence.io grant")
}

// Test_StreamLogsDeniedNamespaceGetsErrorFrame asserts that an identity
// without logs.telepresence.io access still gets BEGIN, a denial error
// frame, and END, rather than an outright refusal.
func (s *AuthGrant) Test_StreamLogsDeniedNamespaceGetsErrorFrame() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGrantIdentity(t, ctx, r, "rtest-auth-grant-logs-denied", telepresenceGrantRules)
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
func (s *AuthGrant) Test_StreamLogsAuthorizedReceivesData() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGrantIdentity(t, ctx, r, "rtest-auth-grant-logs-granted", telepresenceGrantWithLogsRules)
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
func (s *AuthGrant) Test_WatchNamespacesBasics() {
	t := s.T()
	ctx := s.Ctx()
	r := s.R()
	ns := s.AppNamespace()
	s.Manager()

	name := createGrantIdentity(t, ctx, r, "rtest-auth-grant-watchns", telepresenceGrantRules)
	tok := kubectlCreateToken(t, ctx, r, name)
	mc, tokCtx, si := dialAndArrive(t, ctx, r, ns, name, tok)

	stream, err := mc.WatchNamespaces(tokCtx, si)
	s.Require().NoError(err)
	list, err := stream.Recv()
	s.Require().NoError(err)
	s.Contains(list.GetNamespaces(), ns, "the first NamespaceList should include the app namespace")

	garbage := &manager.SessionInfo{SessionId: "rtest-auth-grant-watchns-garbage-session"}
	gStream, err := mc.WatchNamespaces(tokCtx, garbage)
	s.Require().NoError(err)
	_, err = gStream.Recv()
	s.Require().Error(err, "WatchNamespaces with an unknown session id should be refused")
	st, ok := status.FromError(err)
	s.Require().True(ok)
	s.Equal(codes.NotFound, st.Code())
}

package manager

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/clog/handler"
	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// This file covers the required-grant dispatch in authorizeConnect and
// authorizeAttachment (service.go): per-grant denial of a session and of
// PrepareIntercept, grant selectivity (a grant that satisfies one required grant but
// not another), and GrantAny's accept-either behavior including the
// legacy-grant fallback it logs.

// grantTestNamespace is the target namespace used by every test in this file
// for the workload under intercept. It matches the namespace seeded by
// getTestClientConnAndService.
const grantTestNamespace = "default"

// sarRecorder records every SubjectAccessReview ResourceAttributes a test
// observes, in call order, so tests can assert both the shape of individual
// reviews and the order in which GrantAny tries them.
type sarRecorder struct {
	mu      sync.Mutex
	reviews []authv1.ResourceAttributes
}

func (r *sarRecorder) record(ra authv1.ResourceAttributes) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reviews = append(r.reviews, ra)
}

func (r *sarRecorder) all() []authv1.ResourceAttributes {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]authv1.ResourceAttributes(nil), r.reviews...)
}

// installRecordingSAR installs a SubjectAccessReview reactor that records
// every review's ResourceAttributes into rec and decides Allowed using
// allowed.
func installRecordingSAR(cs *fake.Clientset, rec *sarRecorder, allowed func(ra *authv1.ResourceAttributes) bool) {
	cs.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		allow := false
		if review.Spec.ResourceAttributes != nil {
			rec.record(*review.Spec.ResourceAttributes)
			allow = allowed(review.Spec.ResourceAttributes)
		}
		review.Status = authv1.SubjectAccessReviewStatus{Allowed: allow}
		return true, review, nil
	})
}

// isPortForwardReview and isAttachmentReview classify a ResourceAttributes
// by the grant it corresponds to.
func isPortForwardReview(ra *authv1.ResourceAttributes) bool {
	return ra.Group == "" && ra.Resource == "pods" && ra.Subresource == "portforward"
}

func isConnectReview(ra *authv1.ResourceAttributes) bool {
	return ra.Group == "telepresence.io" && ra.Resource == "connections"
}

func isAttachmentReview(ra *authv1.ResourceAttributes) bool {
	return ra.Group == "telepresence.io" && ra.Resource == "attachments"
}

// allowPortForwardOnly simulates an identity whose Role grants only
// pods/portforward -- no telepresence.io attributes at all.
func allowPortForwardOnly(ra *authv1.ResourceAttributes) bool {
	return isPortForwardReview(ra)
}

// allowTelepresenceOnly simulates an identity whose Role grants only the
// telepresence.io group's own attributes -- no pods/portforward.
func allowTelepresenceOnly(ra *authv1.ResourceAttributes) bool {
	return isConnectReview(ra) || isAttachmentReview(ra)
}

// loggingContext returns ctx with a logger that writes to buf at every
// level, so a test can assert on warning text the way
// cmd/traffic/cmd/manager/auth/interceptor_test.go does for the interceptor.
func loggingContext(ctx context.Context, buf *bytes.Buffer) context.Context {
	h := handler.NewText(handler.Output(buf), handler.EnabledLevel(clog.LevelTrace))
	return clog.WithLogger(ctx, slog.New(h))
}

// grantFixtures seeds the Deployment, Service, and Pod for a workload named
// "test-agent" in grantTestNamespace that both grant kinds and PrepareIntercept
// need to resolve.
func grantFixtures() []runtime.Object {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: grantTestNamespace},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "test-agent"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "test-agent"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}}},
				},
			},
		},
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: grantTestNamespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "test-agent"},
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(8080)}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent-abc123", Namespace: grantTestNamespace, Labels: map[string]string{"app": "test-agent"}},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			PodIP:      "10.42.0.5",
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", ContainerID: "containerd://abc123"},
			},
		},
	}
	return []runtime.Object{dep, svc, pod}
}

// interceptRequest builds a node-agent CreateInterceptRequest for the
// "test-agent" Deployment, so PrepareIntercept never needs an agent session
// to have arrived.
func interceptRequest(sess *rpc.SessionInfo, clientName, name string) *rpc.CreateInterceptRequest {
	return &rpc.CreateInterceptRequest{
		Session: sess,
		InterceptSpec: &rpc.InterceptSpec{
			Name:         name,
			Client:       clientName,
			Agent:        "test-agent",
			Namespace:    grantTestNamespace,
			WorkloadKind: string(k8sapi.DeploymentKind),
			NodeAgent:    true,
			Wiretap:      true,
			Mechanism:    "tcp",
		},
	}
}

// writeActions returns the write actions recorded on cs since the since'th
// action, excluding the authorization reviews themselves.
func writeActions(cs *fake.Clientset, since int) []k8stesting.Action {
	var out []k8stesting.Action
	for _, a := range cs.Actions()[since:] {
		switch a.GetVerb() {
		case "create", "update", "patch", "delete":
		default:
			continue
		}
		switch a.GetResource().Resource {
		case "subjectaccessreviews", "selfsubjectaccessreviews":
			continue
		}
		out = append(out, a)
	}
	return out
}

var allGrants = []auth.Grant{auth.GrantPortForward, auth.GrantTelepresence, auth.GrantAny}

// TestGrant_Connect_Denied covers that, for every grant value in
// ModeEnforcing, an authenticated caller whose RBAC grants nothing is
// refused a session, and that the refusal leaves no client session behind.
func TestGrant_Connect_Denied(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)

	for _, grant := range allGrants {
		t.Run(string(grant), func(t *testing.T) {
			req := require.New(t)
			_, mgr, sctx := getTestClientConnAndService(ctx, t, grantFixtures(), func(e *managerutil.Env) {
				e.AuthenticationMode = auth.ModeEnforcing
				e.AuthorizationRequiredGrant = grant
			})
			k8sapi.InstallFakeSubjectAccessReviews(k8sapi.GetK8sInterface(sctx), nil)

			principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
			aliceInfo := testdata.GetTestClients(t)["alice"]

			_, err := mgr.ArriveAsClient(auth.WithPrincipal(sctx, principal), aliceInfo)
			req.Error(err)
			req.Equal(codes.PermissionDenied, status.Code(err))
			req.Equal(0, mgr.State().CountClients(), "a denied connect must not add a client session")
		})
	}
}

// TestGrant_PrepareIntercept_DeniedBeforeMutation: a caller authorized to
// connect but denied on attach is refused at PrepareIntercept before any
// mutating side effect reaches the fake clientset, for every grant.
func TestGrant_PrepareIntercept_DeniedBeforeMutation(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)

	for _, grant := range allGrants {
		t.Run(string(grant), func(t *testing.T) {
			req := require.New(t)
			_, mgr, sctx := getTestClientConnAndService(ctx, t, grantFixtures(), func(e *managerutil.Env) {
				e.AuthenticationMode = auth.ModeEnforcing
				e.AuthorizationRequiredGrant = grant
				e.AgentArrivalTimeout = 5 * time.Second
			})
			mgrNs := managerutil.GetEnv(sctx).ManagerNamespace

			// Allow only reviews in the manager's own namespace, so the
			// session establishes but the attachment review is denied.
			k8sapi.InstallFakeSubjectAccessReviews(k8sapi.GetK8sInterface(sctx), func(_ string, ra *authv1.ResourceAttributes) bool {
				return ra.Namespace == mgrNs
			})

			principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
			pctx := auth.WithPrincipal(sctx, principal)
			aliceInfo := testdata.GetTestClients(t)["alice"]

			sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
			req.NoError(err, "connect must succeed: the manager-namespace grant authorizes it under every required grant")

			cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
			req.True(ok)
			baseline := len(cs.Actions())

			_, err = mgr.PrepareIntercept(pctx, interceptRequest(sess, aliceInfo.Name, "ic1"))
			req.Error(err)
			req.Equal(codes.PermissionDenied, status.Code(err))
			req.Empty(writeActions(cs, baseline), "a denied PrepareIntercept must produce no mutation")
		})
	}
}

// TestGrant_Selectivity: a grant satisfying one required grant does not satisfy
// another (portforward-only vs. telepresence.io-only, under each required grant).
func TestGrant_Selectivity(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)

	tests := []struct {
		name    string
		grant   auth.Grant
		allowed func(*authv1.ResourceAttributes) bool
		passes  bool
	}{
		{"requiredGrant portforward, portforward-only grant passes", auth.GrantPortForward, allowPortForwardOnly, true},
		{"requiredGrant telepresence, telepresence-only grant passes", auth.GrantTelepresence, allowTelepresenceOnly, true},
		{"requiredGrant portforward, telepresence-only grant is denied", auth.GrantPortForward, allowTelepresenceOnly, false},
		{"requiredGrant telepresence, portforward-only grant is denied", auth.GrantTelepresence, allowPortForwardOnly, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)
			_, mgr, sctx := getTestClientConnAndService(ctx, t, grantFixtures(), func(e *managerutil.Env) {
				e.AuthenticationMode = auth.ModeEnforcing
				e.AuthorizationRequiredGrant = tt.grant
				e.AgentArrivalTimeout = 5 * time.Second
				e.NodeAgentEnabled = true
				e.NodeAgentCRISocket = "/run/containerd/containerd.sock"
				e.EnabledWorkloadKinds = k8sapi.Kinds{k8sapi.DeploymentKind}
			})
			sctx = managerutil.WithResolvedAgentImageRetriever(sctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))

			cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
			req.True(ok)
			rec := &sarRecorder{}
			installRecordingSAR(cs, rec, tt.allowed)

			principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
			pctx := auth.WithPrincipal(sctx, principal)
			aliceInfo := testdata.GetTestClients(t)["alice"]

			sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
			if !tt.passes {
				req.Error(err)
				req.Equal(codes.PermissionDenied, status.Code(err))
				return
			}
			req.NoError(err)

			pi, err := mgr.PrepareIntercept(pctx, interceptRequest(sess, aliceInfo.Name, "ic1"))
			req.NoError(err)
			req.Empty(pi.Error, "PrepareIntercept business logic must succeed once authorization passes")

			if tt.grant == auth.GrantTelepresence {
				// The attachment review the manager sent must name the
				// workload as Name, with no subresource.
				var found *authv1.ResourceAttributes
				for _, ra := range rec.all() {
					if isAttachmentReview(&ra) {
						ra := ra
						found = &ra
						break
					}
				}
				req.NotNil(found, "an attachments.telepresence.io review must have been sent")
				req.Empty(found.Subresource)
				req.Equal("test-agent", found.Name)
				req.Equal(grantTestNamespace, found.Namespace)
				req.Equal("create", found.Verb)
			}
		})
	}
}

// TestGrant_Any_AcceptsEither: under GrantAny, a legacy portforward-only grant
// still passes (with a warning logged and the telepresence.io review tried
// first), and a telepresence.io-only grant passes with no warning.
func TestGrant_Any_AcceptsEither(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)

	t.Run("legacy pods/portforward grant passes with a warning", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, grantFixtures(), func(e *managerutil.Env) {
			e.AuthenticationMode = auth.ModeEnforcing
			e.AuthorizationRequiredGrant = auth.GrantAny
			e.AgentArrivalTimeout = 5 * time.Second
			e.NodeAgentEnabled = true
			e.NodeAgentCRISocket = "/run/containerd/containerd.sock"
			e.EnabledWorkloadKinds = k8sapi.Kinds{k8sapi.DeploymentKind}
		})
		sctx = managerutil.WithResolvedAgentImageRetriever(sctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))

		cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
		req.True(ok)
		rec := &sarRecorder{}
		installRecordingSAR(cs, rec, allowPortForwardOnly)

		buf := &bytes.Buffer{}
		principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
		pctx := auth.WithPrincipal(loggingContext(sctx, buf), principal)
		aliceInfo := testdata.GetTestClients(t)["alice"]

		sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
		req.NoError(err)
		req.Contains(buf.String(), "legacy pods/portforward grant", "connect must warn about the legacy-grant fallback")

		connectReviews := rec.all()
		req.GreaterOrEqual(len(connectReviews), 2)
		req.True(isConnectReview(&connectReviews[0]), "the telepresence.io review must be attempted first")
		req.True(isPortForwardReview(&connectReviews[1]), "pods/portforward must be tried only after the telepresence.io review is denied")

		buf.Reset()
		pi, err := mgr.PrepareIntercept(pctx, interceptRequest(sess, aliceInfo.Name, "ic1"))
		req.NoError(err)
		req.Empty(pi.Error)
		req.Contains(buf.String(), "legacy pods/portforward grant", "the attachment review must warn about the legacy-grant fallback too")

		attachReviews := rec.all()[len(connectReviews):]
		req.GreaterOrEqual(len(attachReviews), 2)
		req.True(isAttachmentReview(&attachReviews[0]), "the telepresence.io review must be attempted first for the attachment too")
		req.True(isPortForwardReview(&attachReviews[1]), "pods/portforward must be tried only after the attachment review is denied")
	})

	t.Run("telepresence.io grant passes with no fallback or warning", func(t *testing.T) {
		req := require.New(t)
		_, mgr, sctx := getTestClientConnAndService(ctx, t, grantFixtures(), func(e *managerutil.Env) {
			e.AuthenticationMode = auth.ModeEnforcing
			e.AuthorizationRequiredGrant = auth.GrantAny
			e.AgentArrivalTimeout = 5 * time.Second
			e.NodeAgentEnabled = true
			e.NodeAgentCRISocket = "/run/containerd/containerd.sock"
			e.EnabledWorkloadKinds = k8sapi.Kinds{k8sapi.DeploymentKind}
		})
		sctx = managerutil.WithResolvedAgentImageRetriever(sctx, managerutil.ImageFromEnv("ghcr.io/telepresenceio/tel2:2.99.0"))

		k8sapi.InstallFakeSubjectAccessReviews(k8sapi.GetK8sInterface(sctx), func(_ string, ra *authv1.ResourceAttributes) bool {
			return allowTelepresenceOnly(ra)
		})

		buf := &bytes.Buffer{}
		principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
		pctx := auth.WithPrincipal(loggingContext(sctx, buf), principal)
		aliceInfo := testdata.GetTestClients(t)["alice"]

		sess, err := mgr.ArriveAsClient(pctx, aliceInfo)
		req.NoError(err)
		req.NotContains(buf.String(), "legacy pods/portforward grant")

		buf.Reset()
		pi, err := mgr.PrepareIntercept(pctx, interceptRequest(sess, aliceInfo.Name, "ic1"))
		req.NoError(err)
		req.Empty(pi.Error)
		req.NotContains(buf.String(), "legacy pods/portforward grant")
	})
}

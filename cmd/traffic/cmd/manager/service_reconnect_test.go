package manager

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/clog/testutil"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// errCanned is returned by the SubjectAccessReview reactor in
// TestReconnectClient_UnavailableReview_Fails to simulate an API-server
// failure distinct from an ordinary denial.
var errCanned = errors.New("subject access review: canned failure")

// TestReconnectClient_ForgedPayloadNormalization: a restored intercept is
// rebuilt from the payload's Spec alone, so a forged Id, ClientSession,
// disposition, or runtime state is replaced rather than trusted.
func TestReconnectClient_ForgedPayloadNormalization(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil)

	testClients := testdata.GetTestClients(t)
	alice := testClients["alice"]

	// The session is unknown to this (freshly built) manager, simulating a
	// client reconnecting after the manager lost its state.
	const lostSessionID = "lost-session-1"

	forged := &rpc.InterceptInfo{
		Id: "forged-id:not-the-real-one",
		Spec: &rpc.InterceptSpec{
			Name:      "ic1",
			Client:    alice.Name,
			Agent:     "test-agent",
			Namespace: "default",
			Mechanism: "tcp",
		},
		Disposition:   rpc.InterceptDispositionType_ACTIVE,
		ClientSession: &rpc.SessionInfo{SessionId: "some-other-clients-session"},
		PodIp:         "6.6.6.6",
		PodName:       "forged-pod",
		Environment:   map[string]string{"SECRET": "leaked"},
		Message:       "forged message",
	}

	_, err := mgr.ReconnectClient(sctx, &rpc.ReconnectClientRequest{
		Session:    &rpc.SessionInfo{SessionId: lostSessionID},
		Client:     alice,
		Intercepts: []*rpc.InterceptInfo{forged},
	})
	req.NoError(err)

	// The forged id must never have been used as a storage key.
	_, ok := mgr.State().GetIntercept(forged.Id)
	req.False(ok, "the payload's forged id must not be used to store the intercept")

	stored, ok := mgr.State().GetIntercept(lostSessionID + ":ic1")
	req.True(ok, "the intercept must be restored under the id reconstructed from the session and spec name")
	req.Equal(lostSessionID+":ic1", stored.Id)
	req.Equal(lostSessionID, stored.ClientSession.SessionId, "ClientSession must be the reconnecting session, never the payload's")
	req.Equal(rpc.InterceptDispositionType_WAITING, stored.Disposition)
	req.Empty(stored.PodIp)
	req.Empty(stored.PodName)
	req.Empty(stored.Environment)
	req.Empty(stored.Message)
}

// TestReconnectClient_MixedRestoration_Enforcing: of three intercepts, the
// one no longer authorized is omitted while the other two and the session
// are restored, and the RPC still succeeds.
func TestReconnectClient_MixedRestoration_Enforcing(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModeEnforcing
	})

	// The connect review (create on connections.telepresence.io) always
	// passes; the attachment review passes for every workload except
	// "revoked-agent"; pods/portforward -- the GrantAny fallback -- is denied
	// throughout, so the revoked workload has no other way to pass.
	const revokedAgent = "revoked-agent"
	k8sapi.InstallFakeSubjectAccessReviews(k8sapi.GetK8sInterface(sctx), func(_ string, ra *authv1.ResourceAttributes) bool {
		switch ra.Resource {
		case "connections":
			return true
		case "attachments":
			return ra.Name != revokedAgent
		default:
			return false
		}
	})

	principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, principal)

	testClients := testdata.GetTestClients(t)
	alice := testClients["alice"]

	const lostSessionID = "lost-session-2"
	specFor := func(name, agent string) *rpc.InterceptInfo {
		return &rpc.InterceptInfo{
			Id: lostSessionID + ":" + name,
			Spec: &rpc.InterceptSpec{
				Name:         name,
				Client:       alice.Name,
				Agent:        agent,
				Namespace:    "default",
				WorkloadKind: string(k8sapi.DeploymentKind),
				Mechanism:    "tcp",
			},
			Disposition:   rpc.InterceptDispositionType_WAITING,
			ClientSession: &rpc.SessionInfo{SessionId: lostSessionID},
		}
	}

	_, err := mgr.ReconnectClient(pctx, &rpc.ReconnectClientRequest{
		Session: &rpc.SessionInfo{SessionId: lostSessionID},
		Client:  alice,
		Intercepts: []*rpc.InterceptInfo{
			specFor("ic1", "authorized-agent-1"),
			specFor("ic2", "authorized-agent-2"),
			specFor("ic3", revokedAgent),
		},
	})
	req.NoError(err, "one denied intercept must not fail the reconnect")

	req.NotNil(mgr.State().GetClient(tunnel.SessionID(lostSessionID)), "the session itself must be restored")

	_, ok := mgr.State().GetIntercept(lostSessionID + ":ic1")
	req.True(ok, "an authorized intercept must be restored")
	_, ok = mgr.State().GetIntercept(lostSessionID + ":ic2")
	req.True(ok, "an authorized intercept must be restored")
	_, ok = mgr.State().GetIntercept(lostSessionID + ":ic3")
	req.False(ok, "the intercept targeting a revoked grant must be omitted")
}

// TestReconnectClient_UnavailableReview_Fails: a SubjectAccessReview call
// that errors (not merely denies) fails the whole reconnect rather than
// being treated as a denial.
func TestReconnectClient_UnavailableReview_Fails(t *testing.T) {
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	_, mgr, sctx := getTestClientConnAndService(ctx, t, nil, func(e *managerutil.Env) {
		e.AuthenticationMode = auth.ModeEnforcing
	})

	cs, ok := k8sapi.GetK8sInterface(sctx).(*fake.Clientset)
	req.True(ok)

	// The connect review must succeed so the failure under test is
	// unambiguously the per-intercept review; only the second and later
	// SubjectAccessReview calls (the intercept review) fail.
	calls := 0
	cs.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		calls++
		if calls == 1 {
			review.Status = authv1.SubjectAccessReviewStatus{Allowed: true}
			return true, review, nil
		}
		return true, nil, errCanned
	})

	principal := &auth.Principal{Username: "alice", UID: "alice-uid"}
	pctx := auth.WithPrincipal(sctx, principal)

	testClients := testdata.GetTestClients(t)
	alice := testClients["alice"]

	const lostSessionID = "lost-session-3"
	_, err := mgr.ReconnectClient(pctx, &rpc.ReconnectClientRequest{
		Session: &rpc.SessionInfo{SessionId: lostSessionID},
		Client:  alice,
		Intercepts: []*rpc.InterceptInfo{{
			Id: lostSessionID + ":ic1",
			Spec: &rpc.InterceptSpec{
				Name:         "ic1",
				Client:       alice.Name,
				Agent:        "test-agent",
				Namespace:    "default",
				WorkloadKind: string(k8sapi.DeploymentKind),
				Mechanism:    "tcp",
			},
			Disposition:   rpc.InterceptDispositionType_WAITING,
			ClientSession: &rpc.SessionInfo{SessionId: lostSessionID},
		}},
	})
	req.Error(err)
}

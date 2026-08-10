package auth_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func TestCanPortForward_NamespaceWideAllow(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeSubjectAccessReviews(ci, func(user string, ra *authv1.ResourceAttributes) bool {
		return user == "alice" && ra.Namespace == "ns1" && ra.Name == ""
	})

	a := auth.NewAuthorizer(ci)
	allowed, err := a.CanPortForward(context.Background(), &auth.Principal{Username: "alice"}, "ns1", []string{"pod-a"})
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestCanPortForward_NamespaceWideDeny_PodNameAllow(t *testing.T) {
	var mu sync.Mutex
	var reviews []*authv1.ResourceAttributes
	ci := fake.NewClientset()
	ci.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		ra := review.Spec.ResourceAttributes
		mu.Lock()
		reviews = append(reviews, ra)
		mu.Unlock()
		review.Status = authv1.SubjectAccessReviewStatus{Allowed: ra.Name == "pod-a"}
		return true, review, nil
	})

	a := auth.NewAuthorizer(ci)
	allowed, err := a.CanPortForward(context.Background(), &auth.Principal{Username: "alice"}, "ns1", []string{"pod-a", "pod-b"})
	require.NoError(t, err)
	assert.True(t, allowed)

	// The per-pod reviews run concurrently, so pod-b's may or may not have
	// started before pod-a's allow ended the group.
	names := make([]string, len(reviews))
	for i, ra := range reviews {
		names[i] = ra.Name
	}
	assert.Equal(t, "", names[0], "the namespace-wide review runs first")
	assert.Contains(t, names, "pod-a")
	assert.LessOrEqual(t, len(names), 3)
}

func TestCanPortForward_AllDenied(t *testing.T) {
	ci := fake.NewClientset()
	k8sapi.InstallFakeSubjectAccessReviews(ci, nil)

	a := auth.NewAuthorizer(ci)
	allowed, err := a.CanPortForward(context.Background(), &auth.Principal{Username: "alice"}, "ns1", []string{"pod-a", "pod-b"})
	require.NoError(t, err)
	assert.False(t, allowed)
}

// TestCanPortForward_PropagatesUIDAndExtra verifies that a Principal's UID and Extra
// claims (e.g. the x509 authenticator's credential-id) reach the SubjectAccessReview,
// matching what the API server itself would have set for the same identity.
func TestCanPortForward_PropagatesUIDAndExtra(t *testing.T) {
	var reviews []*authv1.SubjectAccessReviewSpec
	ci := fake.NewClientset()
	ci.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		reviews = append(reviews, &review.Spec)
		review.Status = authv1.SubjectAccessReviewStatus{Allowed: true}
		return true, review, nil
	})

	a := auth.NewAuthorizer(ci)
	p := &auth.Principal{
		Username: "alice",
		UID:      "uid-1",
		Groups:   []string{"g"},
		Extra:    map[string][]string{"authentication.kubernetes.io/credential-id": {"X509SHA256=abc"}},
	}
	allowed, err := a.CanPortForward(context.Background(), p, "ns1", nil)
	require.NoError(t, err)
	assert.True(t, allowed)

	require.Len(t, reviews, 1)
	assert.Equal(t, "uid-1", reviews[0].UID)
	assert.Equal(t, authv1.ExtraValue{"X509SHA256=abc"}, reviews[0].Extra["authentication.kubernetes.io/credential-id"])
}

func TestCanPortForward_APIError(t *testing.T) {
	ci := fake.NewClientset()
	failure := errors.New("connection refused")
	ci.PrependReactor("create", "subjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, failure
	})

	a := auth.NewAuthorizer(ci)
	allowed, err := a.CanPortForward(context.Background(), &auth.Principal{Username: "alice"}, "ns1", nil)
	assert.False(t, allowed)
	require.Error(t, err)
}

// countingSAR installs a SubjectAccessReview reactor that counts reviews and
// answers with allow(ra).
func countingSAR(ci *fake.Clientset, count *atomic.Int32, allow func(*authv1.ResourceAttributes) bool) {
	ci.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		count.Add(1)
		review.Status = authv1.SubjectAccessReviewStatus{Allowed: allow(review.Spec.ResourceAttributes)}
		return true, review, nil
	})
}

// TestAuthorize_AllowedVerdictCached: a repeated review for the same
// principal and attributes is served from the verdict cache.
func TestAuthorize_AllowedVerdictCached(t *testing.T) {
	var count atomic.Int32
	ci := fake.NewClientset()
	countingSAR(ci, &count, func(*authv1.ResourceAttributes) bool { return true })

	a := auth.NewAuthorizer(ci)
	p := &auth.Principal{Username: "alice"}
	for range 3 {
		require.NoError(t, a.Authorize(context.Background(), auth.GrantTelepresence, p, auth.ConnectReview("ns1")))
	}
	assert.Equal(t, int32(1), count.Load())
}

// TestAuthorize_DeniedVerdictCached: a denial is cached too, for the shorter
// failure TTL.
func TestAuthorize_DeniedVerdictCached(t *testing.T) {
	var count atomic.Int32
	ci := fake.NewClientset()
	countingSAR(ci, &count, func(*authv1.ResourceAttributes) bool { return false })

	a := auth.NewAuthorizer(ci)
	p := &auth.Principal{Username: "alice"}
	for range 2 {
		err := a.Authorize(context.Background(), auth.GrantTelepresence, p, auth.ConnectReview("ns1"))
		require.Error(t, err)
		assert.Equal(t, codes.PermissionDenied, status.Code(err))
	}
	assert.Equal(t, int32(1), count.Load())
}

// TestAuthorize_ReviewErrorNotCached: an Unavailable outcome is never
// cached; the next call reviews again.
func TestAuthorize_ReviewErrorNotCached(t *testing.T) {
	var count atomic.Int32
	ci := fake.NewClientset()
	ci.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if count.Add(1) == 1 {
			return true, nil, errors.New("api server down")
		}
		review := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		review.Status = authv1.SubjectAccessReviewStatus{Allowed: true}
		return true, review, nil
	})

	a := auth.NewAuthorizer(ci)
	p := &auth.Principal{Username: "alice"}
	err := a.Authorize(context.Background(), auth.GrantTelepresence, p, auth.ConnectReview("ns1"))
	require.Error(t, err)
	assert.Equal(t, codes.Unavailable, status.Code(err))
	require.NoError(t, a.Authorize(context.Background(), auth.GrantTelepresence, p, auth.ConnectReview("ns1")))
	assert.Equal(t, int32(2), count.Load())
}

// TestAuthorize_VerdictCacheKeyedByPrincipal: different principals never
// share a cached verdict.
func TestAuthorize_VerdictCacheKeyedByPrincipal(t *testing.T) {
	var count atomic.Int32
	ci := fake.NewClientset()
	countingSAR(ci, &count, func(*authv1.ResourceAttributes) bool { return true })

	a := auth.NewAuthorizer(ci)
	require.NoError(t, a.Authorize(context.Background(), auth.GrantTelepresence, &auth.Principal{Username: "alice"}, auth.ConnectReview("ns1")))
	require.NoError(t, a.Authorize(context.Background(), auth.GrantTelepresence, &auth.Principal{Username: "bob"}, auth.ConnectReview("ns1")))
	assert.Equal(t, int32(2), count.Load())
}

// TestAuthorize_EnsureAgentAcceptsCreateOrGet: the ensure-agent review
// passes when either the create or the get attachment attribute is allowed.
func TestAuthorize_EnsureAgentAcceptsCreateOrGet(t *testing.T) {
	var count atomic.Int32
	ci := fake.NewClientset()
	countingSAR(ci, &count, func(ra *authv1.ResourceAttributes) bool {
		return ra.Resource == "attachments" && ra.Verb == "get"
	})

	a := auth.NewAuthorizer(ci)
	p := &auth.Principal{Username: "alice"}
	require.NoError(t, a.Authorize(context.Background(), auth.GrantTelepresence, p, auth.EnsureAgentReview("ns1", "echo")))
	assert.Equal(t, int32(2), count.Load(), "create denied, then get allowed")
}

// TestAuthorize_EnsureAgentPortForwardReviewedOnce: under the portforward
// grant, the two-verb ensure-agent review costs a single legacy review.
func TestAuthorize_EnsureAgentPortForwardReviewedOnce(t *testing.T) {
	var count atomic.Int32
	ci := fake.NewClientset()
	countingSAR(ci, &count, func(ra *authv1.ResourceAttributes) bool {
		return ra.Resource == "pods" && ra.Subresource == "portforward"
	})

	a := auth.NewAuthorizer(ci)
	p := &auth.Principal{Username: "alice"}
	require.NoError(t, a.Authorize(context.Background(), auth.GrantPortForward, p, auth.EnsureAgentReview("ns1", "echo")))
	assert.Equal(t, int32(1), count.Load())
}

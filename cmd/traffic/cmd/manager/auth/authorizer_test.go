package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	var reviews []*authv1.ResourceAttributes
	ci := fake.NewClientset()
	ci.PrependReactor("create", "subjectaccessreviews", func(action k8stesting.Action) (bool, runtime.Object, error) {
		review := action.(k8stesting.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		ra := review.Spec.ResourceAttributes
		reviews = append(reviews, ra)
		review.Status = authv1.SubjectAccessReviewStatus{Allowed: ra.Name == "pod-a"}
		return true, review, nil
	})

	a := auth.NewAuthorizer(ci)
	allowed, err := a.CanPortForward(context.Background(), &auth.Principal{Username: "alice"}, "ns1", []string{"pod-a", "pod-b"})
	require.NoError(t, err)
	assert.True(t, allowed)

	require.Len(t, reviews, 2)
	assert.Equal(t, "", reviews[0].Name)
	assert.Equal(t, "pod-a", reviews[1].Name)
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

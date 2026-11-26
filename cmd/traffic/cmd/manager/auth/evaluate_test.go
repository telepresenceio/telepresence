package auth_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/authz"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// TestEvaluate_AgreesWithAuthorize runs every review the manager authorizes
// with, under each grant mode, once through the manager's Authorize and once
// through authz.Evaluate with SelfSubjectAccessReviews, for identities that
// hold different grants. Both must reach the same verdict.
func TestEvaluate_AgreesWithAuthorize(t *testing.T) {
	isTelepresence := func(ra *authv1.ResourceAttributes) bool { return ra.Group == "telepresence.io" }
	isPortForward := func(ra *authv1.ResourceAttributes) bool {
		return ra.Resource == "pods" && ra.Subresource == "portforward"
	}
	identities := map[string]func(*authv1.ResourceAttributes) bool{
		"nothing":           func(*authv1.ResourceAttributes) bool { return false },
		"telepresence only": isTelepresence,
		"portforward only":  isPortForward,
		"both":              func(ra *authv1.ResourceAttributes) bool { return isTelepresence(ra) || isPortForward(ra) },
		"get attachments only": func(ra *authv1.ResourceAttributes) bool {
			return ra.Resource == "attachments" && ra.Verb == "get"
		},
		"one workload only": func(ra *authv1.ResourceAttributes) bool {
			return ra.Resource == "attachments" && ra.Name == "echo"
		},
		"portforward on one pod": func(ra *authv1.ResourceAttributes) bool {
			return isPortForward(ra) && ra.Name == "echo-pod"
		},
	}
	reviews := map[string]func() *authz.Review{
		"connect":           func() *authz.Review { return authz.ConnectReview("ns1") },
		"intercept":         func() *authz.Review { return authz.AttachmentReview("ns1", "echo", "create") },
		"ingest":            func() *authz.Review { return authz.AttachmentReview("ns1", "echo", "get") },
		"other workload":    func() *authz.Review { return authz.AttachmentReview("ns1", "other", "create") },
		"ensure agent":      func() *authz.Review { return authz.EnsureAgentReview("ns1", "echo") },
		"namespace read":    func() *authz.Review { return authz.NamespaceReadReview("ns1") },
		"namespace":         func() *authz.Review { return authz.NamespaceReview("ns1") },
		"grounded on a pod": groundedReview,
	}
	grants := []authz.Grant{authz.GrantPortForward, authz.GrantTelepresence, authz.GrantAny}

	for idName, allow := range identities {
		sar := fake.NewClientset()
		k8sapi.InstallFakeSubjectAccessReviews(sar, func(_ string, ra *authv1.ResourceAttributes) bool { return allow(ra) })
		ssar := fake.NewClientset()
		k8sapi.InstallFakeSelfSubjectAccessReviews(ssar, allow)
		authorizer := auth.NewAuthorizer(sar)
		p := &auth.Principal{Username: "alice"}

		for reviewName, review := range reviews {
			for _, grant := range grants {
				t.Run(fmt.Sprintf("%s/%s/%s", idName, reviewName, grant), func(t *testing.T) {
					managerErr := authorizer.Authorize(context.Background(), grant, p, review())
					v, err := authz.Evaluate(context.Background(), grant, review(), authz.SelfChecker(ssar))
					require.NoError(t, err)
					assert.Equal(t, managerErr == nil, v.Allowed, "manager verdict: %v", managerErr)
				})
			}
		}
	}
}

func groundedReview() *authz.Review {
	r := authz.AttachmentReview("ns1", "echo", "create")
	r.PodNames = func(context.Context) ([]string, error) { return []string{"other-pod", "echo-pod"}, nil }
	return r
}

func TestEvaluate_ReportsDecidingGrant(t *testing.T) {
	ssar := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(ssar, func(ra *authv1.ResourceAttributes) bool {
		return ra.Subresource == "portforward"
	})
	check := authz.SelfChecker(ssar)

	v, err := authz.Evaluate(context.Background(), authz.GrantAny, authz.ConnectReview("ns1"), check)
	require.NoError(t, err)
	assert.Equal(t, authz.Verdict{Allowed: true, Grant: authz.GrantPortForward}, v)

	v, err = authz.Evaluate(context.Background(), authz.GrantTelepresence, authz.ConnectReview("ns1"), check)
	require.NoError(t, err)
	assert.Equal(t, authz.Verdict{Grant: authz.GrantTelepresence}, v)
}

func TestEvaluate_ReviewError(t *testing.T) {
	// Without a reactor, every SelfSubjectAccessReview fails.
	check := authz.SelfChecker(fake.NewClientset())
	_, err := authz.Evaluate(context.Background(), authz.GrantAny, authz.ConnectReview("ns1"), check)
	var reviewErr *authz.ReviewError
	require.ErrorAs(t, err, &reviewErr)
	assert.Equal(t, authz.GrantTelepresence, reviewErr.Grant)
}

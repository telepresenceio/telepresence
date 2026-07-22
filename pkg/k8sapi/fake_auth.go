package k8sapi

import (
	authnv1 "k8s.io/api/authentication/v1"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/testing"
)

// InstallFakeSelfSubjectAccessReviews registers a reactor on a fake clientset so
// AuthorizationV1().SelfSubjectAccessReviews().Create works. Without it, client-go
// fails with "no type found matching: io.k8s.api.authorization.v1.SelfSubjectAccessReview".
//
// If allowed is nil, all access reviews are denied (typical for namespace-scoped tests).
func InstallFakeSelfSubjectAccessReviews(client kubernetes.Interface, allowed func(*authv1.ResourceAttributes) bool) {
	cs, ok := client.(*fake.Clientset)
	if !ok {
		return
	}
	if allowed == nil {
		allowed = func(*authv1.ResourceAttributes) bool { return false }
	}
	cs.PrependReactor("create", "selfsubjectaccessreviews", func(action testing.Action) (bool, runtime.Object, error) {
		review := action.(testing.CreateAction).GetObject().(*authv1.SelfSubjectAccessReview)
		allow := false
		if review.Spec.ResourceAttributes != nil {
			allow = allowed(review.Spec.ResourceAttributes)
		}
		review.Status = authv1.SubjectAccessReviewStatus{Allowed: allow}
		return true, review, nil
	})
}

// InstallFakeSubjectAccessReviews registers a reactor on a fake clientset so
// AuthorizationV1().SubjectAccessReviews().Create works. Without it, client-go
// fails with "no type found matching: io.k8s.api.authorization.v1.SubjectAccessReview".
//
// If allowed is nil, all access reviews are denied.
func InstallFakeSubjectAccessReviews(client kubernetes.Interface, allowed func(user string, ra *authv1.ResourceAttributes) bool) {
	cs, ok := client.(*fake.Clientset)
	if !ok {
		return
	}
	if allowed == nil {
		allowed = func(string, *authv1.ResourceAttributes) bool { return false }
	}
	cs.PrependReactor("create", "subjectaccessreviews", func(action testing.Action) (bool, runtime.Object, error) {
		review := action.(testing.CreateAction).GetObject().(*authv1.SubjectAccessReview)
		allow := false
		if review.Spec.ResourceAttributes != nil {
			allow = allowed(review.Spec.User, review.Spec.ResourceAttributes)
		}
		review.Status = authv1.SubjectAccessReviewStatus{Allowed: allow}
		return true, review, nil
	})
}

// InstallFakeTokenReviews registers a reactor on a fake clientset so
// AuthenticationV1().TokenReviews().Create works. Without it, client-go
// fails with "no type found matching: io.k8s.api.authentication.v1.TokenReview".
//
// If review is nil, all token reviews are denied.
func InstallFakeTokenReviews(client kubernetes.Interface, review func(token string, audiences []string) *authnv1.TokenReviewStatus) {
	cs, ok := client.(*fake.Clientset)
	if !ok {
		return
	}
	if review == nil {
		review = func(string, []string) *authnv1.TokenReviewStatus {
			return &authnv1.TokenReviewStatus{Authenticated: false}
		}
	}
	cs.PrependReactor("create", "tokenreviews", func(action testing.Action) (bool, runtime.Object, error) {
		tr := action.(testing.CreateAction).GetObject().(*authnv1.TokenReview)
		tr.Status = *review(tr.Spec.Token, tr.Spec.Audiences)
		return true, tr, nil
	})
}

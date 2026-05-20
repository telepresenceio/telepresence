package k8sapi

import (
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

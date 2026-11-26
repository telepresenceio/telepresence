package authz

import (
	"context"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// SelfChecker returns a Checker that reviews access for the identity of
// client with SelfSubjectAccessReviews.
func SelfChecker(client kubernetes.Interface) Checker {
	return func(ctx context.Context, ra *authorizationv1.ResourceAttributes) (bool, error) {
		review := &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: ra},
		}
		result, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		if err != nil {
			return false, err
		}
		return result.Status.Allowed, nil
	}
}

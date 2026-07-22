package auth

import (
	"context"
	"fmt"

	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Authorizer checks a Principal's Kubernetes RBAC with SubjectAccessReviews.
type Authorizer struct {
	client kubernetes.Interface
}

// NewAuthorizer creates an Authorizer that reviews access using ci.
func NewAuthorizer(ci kubernetes.Interface) *Authorizer {
	return &Authorizer{client: ci}
}

// CanPortForward reports whether p may create pods/portforward in namespace.
// It first checks namespace-wide access and falls back to checking each of
// podNames, so grants scoped to specific pod names -- which an empty-name SAR
// never matches -- are honored too.
func (a *Authorizer) CanPortForward(ctx context.Context, p *Principal, namespace string, podNames []string) (bool, error) {
	allowed, err := a.canI(ctx, p, namespace, "")
	if err != nil {
		return false, err
	}
	if allowed {
		return true, nil
	}
	for _, pod := range podNames {
		allowed, err = a.canI(ctx, p, namespace, pod)
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
}

func (a *Authorizer) canI(ctx context.Context, p *Principal, namespace, podName string) (bool, error) {
	review := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   p.Username,
			UID:    p.UID,
			Groups: p.Groups,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Namespace:   namespace,
				Verb:        "create",
				Resource:    "pods",
				Subresource: "portforward",
				Name:        podName,
			},
		},
	}
	result, err := a.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("subject access review: %w", err)
	}
	return result.Status.Allowed, nil
}

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
	return a.review(ctx, p, &authorizationv1.ResourceAttributes{
		Namespace:   namespace,
		Verb:        "create",
		Resource:    "pods",
		Subresource: "portforward",
		Name:        podName,
	})
}

// CanConnect reports whether p may create connections.telepresence.io in
// namespace -- the review that authorizes establishing a session.
func (a *Authorizer) CanConnect(ctx context.Context, p *Principal, namespace string) (bool, error) {
	return a.review(ctx, p, &authorizationv1.ResourceAttributes{
		Namespace: namespace,
		Verb:      "create",
		Group:     "telepresence.io",
		Resource:  "connections",
	})
}

// CanAttach reports whether p may perform verb on the attachments.telepresence.io
// resource named workloadName in namespace. verb is "create" to authorize an
// intercept or "get" to authorize an ingest.
func (a *Authorizer) CanAttach(ctx context.Context, p *Principal, namespace, workloadName, verb string) (bool, error) {
	return a.review(ctx, p, &authorizationv1.ResourceAttributes{
		Namespace: namespace,
		Verb:      verb,
		Group:     "telepresence.io",
		Resource:  "attachments",
		Name:      workloadName,
	})
}

// CanGetLogs reports whether p may get logs.telepresence.io in namespace --
// the review that authorizes streaming that namespace's pod logs. This is a
// diagnostic attribute, not the connect/attachment gate: it is reviewed the
// same way regardless of the configured Gate.
func (a *Authorizer) CanGetLogs(ctx context.Context, p *Principal, namespace string) (bool, error) {
	return a.review(ctx, p, &authorizationv1.ResourceAttributes{
		Namespace: namespace,
		Verb:      "get",
		Group:     "telepresence.io",
		Resource:  "logs",
	})
}

// CanGetLogsYAML reports whether p may get the yaml subresource of
// logs.telepresence.io in namespace -- the review that authorizes including
// a pod's manifest in a StreamLogs response, independent of and in addition
// to CanGetLogs.
func (a *Authorizer) CanGetLogsYAML(ctx context.Context, p *Principal, namespace string) (bool, error) {
	return a.review(ctx, p, &authorizationv1.ResourceAttributes{
		Namespace:   namespace,
		Verb:        "get",
		Group:       "telepresence.io",
		Resource:    "logs",
		Subresource: "yaml",
	})
}

func (a *Authorizer) review(ctx context.Context, p *Principal, ra *authorizationv1.ResourceAttributes) (bool, error) {
	review := &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:               p.Username,
			UID:                p.UID,
			Groups:             p.Groups,
			Extra:              extraValues(p.Extra),
			ResourceAttributes: ra,
		},
	}
	result, err := a.client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("subject access review: %w", err)
	}
	return result.Status.Allowed, nil
}

// extraValues converts a Principal's Extra claims to the type SubjectAccessReviewSpec
// requires. Returns nil for an empty map so an unset Extra doesn't marshal as {}.
func extraValues(extra map[string][]string) map[string]authorizationv1.ExtraValue {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string]authorizationv1.ExtraValue, len(extra))
	for k, v := range extra {
		out[k] = authorizationv1.ExtraValue(v)
	}
	return out
}

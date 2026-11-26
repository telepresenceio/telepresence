package auth

import (
	"context"
	stderrors "errors"
	"fmt"

	"google.golang.org/grpc/codes"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/authz"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
)

// Authorizer checks a Principal's Kubernetes RBAC with SubjectAccessReviews.
type Authorizer struct {
	client kubernetes.Interface
	cache  *sarCache
}

// NewAuthorizer creates an Authorizer that reviews access using ci.
func NewAuthorizer(ci kubernetes.Interface) *Authorizer {
	return &Authorizer{client: ci, cache: newSARCache()}
}

// checker returns the Checker that reviews access for p.
func (a *Authorizer) checker(p *Principal) authz.Checker {
	return func(ctx context.Context, ra *authorizationv1.ResourceAttributes) (bool, error) {
		return a.review(ctx, p, ra)
	}
}

// CanPortForward reports whether p may create pods/portforward in namespace.
// It first checks namespace-wide access and falls back to reviewing each of
// podNames concurrently, so grants scoped to specific pod names -- which an
// empty-name SAR never matches -- are honored too.
func (a *Authorizer) CanPortForward(ctx context.Context, p *Principal, namespace string, podNames []string) (bool, error) {
	return authz.PortForwardAllowed(ctx, a.checker(p), namespace, podNames)
}

// Authorize reviews r for p under required, see authz.Evaluate, and logs
// r.Migration when GrantAny authorizes via the legacy grant alone. The
// verdict is nil, PermissionDenied, or Unavailable when a review could not
// be performed.
func (a *Authorizer) Authorize(ctx context.Context, required authz.Grant, p *Principal, r *authz.Review) error {
	v, err := authz.Evaluate(ctx, required, r, a.checker(p))
	var reviewErr *authz.ReviewError
	switch {
	case stderrors.As(err, &reviewErr):
		return errors.Errorf(codes.Unavailable,
			"unable to determine whether %s may %s in namespace %s: %v", p.Username, r.SubjectFor(reviewErr.Grant), r.Namespace, reviewErr.Err)
	case err != nil:
		return err
	case !v.Allowed:
		return errors.Errorf(codes.PermissionDenied,
			"%s is not permitted to %s in namespace %s", p.Username, r.SubjectFor(v.Grant), r.Namespace)
	}
	if required != authz.GrantPortForward && v.Grant == authz.GrantPortForward {
		clog.Warnf(ctx, "%s %s", p.Username, r.Migration)
	}
	return nil
}

// CanGetLogs reports whether p may get logs.telepresence.io in namespace,
// qualified by subresource when non-empty, see authz.LogsAttributes.
func (a *Authorizer) CanGetLogs(ctx context.Context, p *Principal, namespace, subresource string) (bool, error) {
	return a.review(ctx, p, authz.LogsAttributes(namespace, subresource))
}

// review returns the cached verdict for the principal and attribute set, or
// performs a SubjectAccessReview and caches its outcome.
func (a *Authorizer) review(ctx context.Context, p *Principal, ra *authorizationv1.ResourceAttributes) (bool, error) {
	key := sarKey(p, ra)
	if allowed, ok := a.cache.get(key); ok {
		return allowed, nil
	}
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
	a.cache.put(key, result.Status.Allowed)
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

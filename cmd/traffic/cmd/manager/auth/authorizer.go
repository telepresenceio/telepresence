package auth

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
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

// Review describes one operation's authorization review: the telepresence.io
// policy attributes that govern it, the pods that ground its legacy
// pods/portforward equivalent, and the words used to report the outcome. The
// configured Grant decides which of the two grants can authorize the
// operation.
type Review struct {
	// Subject is the telepresence.io grant as it reads in errors, e.g.
	// "create connections.telepresence.io".
	Subject string
	// Namespace scopes both reviews.
	Namespace string
	// Attributes is the operation's telepresence.io attribute set.
	Attributes *authorizationv1.ResourceAttributes
	// PodNames supplies the pods for the pods/portforward review; nil
	// reviews namespace-wide access only, and an error aborts the review.
	PodNames func(context.Context) ([]string, error)
	// Migration is logged when GrantAny authorizes via the legacy grant
	// alone.
	Migration string
}

// ConnectReview is the Review authorizing a session in the manager's
// namespace.
func ConnectReview(namespace string) *Review {
	return &Review{
		Subject:   "create connections.telepresence.io",
		Namespace: namespace,
		Attributes: &authorizationv1.ResourceAttributes{
			Namespace: namespace,
			Verb:      "create",
			Group:     "telepresence.io",
			Resource:  "connections",
		},
		Migration: "authorized to connect only via the legacy pods/portforward grant; its Role should migrate to " +
			"create connections.telepresence.io before the enforcing-mode default flips",
	}
}

// AttachmentReview is the Review authorizing verb ("create" for an
// intercept, "get" for an ingest) on the attachment named workloadName.
func AttachmentReview(namespace, workloadName, verb string) *Review {
	return &Review{
		Subject:   fmt.Sprintf("%s attachment %s", verb, workloadName),
		Namespace: namespace,
		Attributes: &authorizationv1.ResourceAttributes{
			Namespace: namespace,
			Verb:      verb,
			Group:     "telepresence.io",
			Resource:  "attachments",
			Name:      workloadName,
		},
		Migration: fmt.Sprintf("authorized attachment to %s in namespace %s only via the legacy pods/portforward "+
			"grant; its Role should migrate to create attachments.telepresence.io before the enforcing-mode "+
			"default flips", workloadName, namespace),
	}
}

// NamespaceReview is the Review authorizing attachment to anything in
// namespace: an unnamed attachments review, which a grant scoped with
// resourceNames never matches.
func NamespaceReview(namespace string) *Review {
	return &Review{
		Subject:   "create attachments",
		Namespace: namespace,
		Attributes: &authorizationv1.ResourceAttributes{
			Namespace: namespace,
			Verb:      "create",
			Group:     "telepresence.io",
			Resource:  "attachments",
		},
		Migration: fmt.Sprintf("authorized for namespace %s only via the legacy pods/portforward grant; its Role "+
			"should migrate to create attachments.telepresence.io before the enforcing-mode default flips", namespace),
	}
}

// Authorize reviews r for p under required: GrantPortForward consults only
// the legacy pods/portforward grant, GrantTelepresence only r.Attributes,
// and GrantAny accepts either, logging r.Migration when only the legacy
// grant passes. The verdict is nil, PermissionDenied, or Unavailable when a
// review could not be performed.
func (a *Authorizer) Authorize(ctx context.Context, required Grant, p *Principal, r *Review) error {
	switch required {
	case GrantPortForward:
		return a.portForwardVerdict(ctx, p, r)
	case GrantTelepresence:
		return a.telepresenceVerdict(ctx, p, r)
	default: // GrantAny
		err := a.telepresenceVerdict(ctx, p, r)
		if err == nil || status.Code(err) == codes.Unavailable {
			return err
		}
		if err := a.portForwardVerdict(ctx, p, r); err != nil {
			return err
		}
		clog.Warnf(ctx, "%s %s", p.Username, r.Migration)
		return nil
	}
}

// telepresenceVerdict converts the r.Attributes review into a verdict error.
func (a *Authorizer) telepresenceVerdict(ctx context.Context, p *Principal, r *Review) error {
	allowed, err := a.review(ctx, p, r.Attributes)
	if err != nil {
		return errors.Errorf(codes.Unavailable,
			"unable to determine whether %s may %s in namespace %s: %v", p.Username, r.Subject, r.Namespace, err)
	}
	if !allowed {
		return errors.Errorf(codes.PermissionDenied,
			"%s is not permitted to %s in namespace %s", p.Username, r.Subject, r.Namespace)
	}
	return nil
}

// portForwardVerdict converts the legacy pods/portforward review into a
// verdict error.
func (a *Authorizer) portForwardVerdict(ctx context.Context, p *Principal, r *Review) error {
	var podNames []string
	if r.PodNames != nil {
		var err error
		if podNames, err = r.PodNames(ctx); err != nil {
			return err
		}
	}
	allowed, err := a.CanPortForward(ctx, p, r.Namespace, podNames)
	if err != nil {
		return errors.Errorf(codes.Unavailable,
			"unable to determine whether %s may create pods/portforward in namespace %s: %v", p.Username, r.Namespace, err)
	}
	if !allowed {
		return errors.Errorf(codes.PermissionDenied,
			"%s is not permitted to create pods/portforward in namespace %s", p.Username, r.Namespace)
	}
	return nil
}

// CanGetLogs reports whether p may get logs.telepresence.io in namespace,
// qualified by subresource when non-empty ("yaml" authorizes including a
// pod's manifest in the response). This is a diagnostic attribute, not the
// connect/attachment gate: it is reviewed the same way regardless of the
// configured Gate.
func (a *Authorizer) CanGetLogs(ctx context.Context, p *Principal, namespace, subresource string) (bool, error) {
	return a.review(ctx, p, &authorizationv1.ResourceAttributes{
		Namespace:   namespace,
		Verb:        "get",
		Group:       "telepresence.io",
		Resource:    "logs",
		Subresource: subresource,
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

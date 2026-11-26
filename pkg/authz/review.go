package authz

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

	authorizationv1 "k8s.io/api/authorization/v1"
)

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
	// Attributes are the operation's telepresence.io attribute sets; any
	// one of them being allowed authorizes the operation.
	Attributes []*authorizationv1.ResourceAttributes
	// PodNames supplies the pods for the pods/portforward review; nil
	// reviews namespace-wide access only, and an error aborts the review.
	PodNames func(context.Context) ([]string, error)
	// Migration is logged when GrantAny authorizes via the legacy grant
	// alone.
	Migration string
}

// SubjectFor returns how the review of grant g reads in errors.
func (r *Review) SubjectFor(g Grant) string {
	if g == GrantPortForward {
		return "create pods/portforward"
	}
	return r.Subject
}

// ConnectReview is the Review authorizing a session in the manager's
// namespace.
func ConnectReview(namespace string) *Review {
	return &Review{
		Subject:   "create connections.telepresence.io",
		Namespace: namespace,
		Attributes: []*authorizationv1.ResourceAttributes{{
			Namespace: namespace,
			Verb:      "create",
			Group:     "telepresence.io",
			Resource:  "connections",
		}},
		Migration: "authorized to connect only via the legacy pods/portforward grant; its Role should migrate to " +
			"create connections.telepresence.io, which enforcing mode requires",
	}
}

// AttachmentReview is the Review authorizing verb ("create" for an
// intercept, "get" for an ingest) on the attachment named workloadName.
func AttachmentReview(namespace, workloadName, verb string) *Review {
	return &Review{
		Subject:   fmt.Sprintf("%s attachment %s", verb, workloadName),
		Namespace: namespace,
		Attributes: []*authorizationv1.ResourceAttributes{{
			Namespace: namespace,
			Verb:      verb,
			Group:     "telepresence.io",
			Resource:  "attachments",
			Name:      workloadName,
		}},
		Migration: fmt.Sprintf("authorized attachment to %s in namespace %s only via the legacy pods/portforward "+
			"grant; its Role should migrate to create attachments.telepresence.io which enforcing "+
			"mode requires", workloadName, namespace),
	}
}

// EnsureAgentReview is the Review authorizing agent provisioning for
// workloadName: the caller must hold create (an intercepting client) or get
// (an ingest client) on the attachment.
func EnsureAgentReview(namespace, workloadName string) *Review {
	r := AttachmentReview(namespace, workloadName, "create")
	r.Subject = fmt.Sprintf("create or get attachment %s", workloadName)
	get := *r.Attributes[0]
	get.Verb = "get"
	r.Attributes = append(r.Attributes, &get)
	return r
}

// NamespaceReview is the Review authorizing attachment to anything in
// namespace: an unnamed attachments review, which a grant scoped with
// resourceNames never matches.
func NamespaceReview(namespace string) *Review {
	return &Review{
		Subject:   "create attachments",
		Namespace: namespace,
		Attributes: []*authorizationv1.ResourceAttributes{{
			Namespace: namespace,
			Verb:      "create",
			Group:     "telepresence.io",
			Resource:  "attachments",
		}},
		Migration: fmt.Sprintf("authorized for namespace %s only via the legacy pods/portforward grant; its Role "+
			"should migrate to create attachments.telepresence.io which enforcing mode requires", namespace),
	}
}

// NamespaceReadReview is the Review authorizing reads of everything in
// namespace: an unnamed attachments review for either create or get, which a
// grant scoped with resourceNames never matches.
func NamespaceReadReview(namespace string) *Review {
	r := NamespaceReview(namespace)
	r.Subject = "create or get attachments"
	get := *r.Attributes[0]
	get.Verb = "get"
	r.Attributes = append(r.Attributes, &get)
	return r
}

// LogsAttributes are the attributes for getting logs.telepresence.io in
// namespace, qualified by subresource when non-empty ("yaml" authorizes
// including a pod's manifest in the response). This is a diagnostic
// attribute, not the connect/attachment gate: it is reviewed the same way
// regardless of the configured Grant.
func LogsAttributes(namespace, subresource string) *authorizationv1.ResourceAttributes {
	return &authorizationv1.ResourceAttributes{
		Namespace:   namespace,
		Verb:        "get",
		Group:       "telepresence.io",
		Resource:    "logs",
		Subresource: subresource,
	}
}

// Checker answers one access review for a fixed identity.
type Checker func(ctx context.Context, ra *authorizationv1.ResourceAttributes) (bool, error)

// Verdict is the outcome of Evaluate. Grant is GrantTelepresence or
// GrantPortForward: the grant that allowed the operation, or the last one
// that denied it.
type Verdict struct {
	Allowed bool
	Grant   Grant
}

// ReviewError is returned by Evaluate when a review of Grant could not be
// performed.
type ReviewError struct {
	Grant Grant
	Err   error
}

func (e *ReviewError) Error() string {
	return fmt.Sprintf("unable to review %s: %v", e.Grant, e.Err)
}

func (e *ReviewError) Unwrap() error {
	return e.Err
}

// Evaluate reviews r under required with check: GrantPortForward consults
// only the legacy pods/portforward grant, GrantTelepresence only
// r.Attributes, and GrantAny accepts either, trying r.Attributes first. An
// error is a *ReviewError, or the error from r.PodNames.
func Evaluate(ctx context.Context, required Grant, r *Review, check Checker) (Verdict, error) {
	switch required {
	case GrantPortForward:
		return portForwardVerdict(ctx, r, check)
	case GrantTelepresence:
		return telepresenceVerdict(ctx, r, check)
	default: // GrantAny
		v, err := telepresenceVerdict(ctx, r, check)
		if err != nil || v.Allowed {
			return v, err
		}
		return portForwardVerdict(ctx, r, check)
	}
}

// telepresenceVerdict reviews r.Attributes; any allowed attribute set
// authorizes the operation.
func telepresenceVerdict(ctx context.Context, r *Review, check Checker) (Verdict, error) {
	for _, ra := range r.Attributes {
		allowed, err := check(ctx, ra)
		if err != nil {
			return Verdict{Grant: GrantTelepresence}, &ReviewError{Grant: GrantTelepresence, Err: err}
		}
		if allowed {
			return Verdict{Allowed: true, Grant: GrantTelepresence}, nil
		}
	}
	return Verdict{Grant: GrantTelepresence}, nil
}

// portForwardVerdict reviews the legacy pods/portforward grant on the pods
// from r.PodNames.
func portForwardVerdict(ctx context.Context, r *Review, check Checker) (Verdict, error) {
	var podNames []string
	if r.PodNames != nil {
		var err error
		if podNames, err = r.PodNames(ctx); err != nil {
			return Verdict{Grant: GrantPortForward}, err
		}
	}
	allowed, err := PortForwardAllowed(ctx, check, r.Namespace, podNames)
	if err != nil {
		return Verdict{Grant: GrantPortForward}, &ReviewError{Grant: GrantPortForward, Err: err}
	}
	return Verdict{Allowed: allowed, Grant: GrantPortForward}, nil
}

// perPodReviewConcurrency bounds the concurrent per-pod portforward reviews.
const perPodReviewConcurrency = 4

// errPodAllowed short-circuits the per-pod review group on the first allow.
var errPodAllowed = errors.New("a pod-scoped grant allowed the review")

// PortForwardAllowed reports whether check allows create pods/portforward
// in namespace. It first checks namespace-wide access and falls back to
// reviewing each of podNames concurrently, so grants scoped to specific pod
// names, which an empty-name review never matches, are honored too.
func PortForwardAllowed(ctx context.Context, check Checker, namespace string, podNames []string) (bool, error) {
	allowed, err := check(ctx, PortForwardAttributes(namespace, ""))
	if err != nil {
		return false, err
	}
	if allowed || len(podNames) == 0 {
		return allowed, nil
	}
	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(perPodReviewConcurrency)
	for _, pod := range podNames {
		g.Go(func() error {
			allowed, err := check(gCtx, PortForwardAttributes(namespace, pod))
			if err != nil {
				return err
			}
			if allowed {
				return errPodAllowed
			}
			return nil
		})
	}
	switch err := g.Wait(); {
	case err == nil:
		return false, nil
	case errors.Is(err, errPodAllowed):
		return true, nil
	default:
		return false, err
	}
}

// PortForwardAttributes are the attributes for create pods/portforward on
// podName in namespace, or on any pod when podName is empty.
func PortForwardAttributes(namespace, podName string) *authorizationv1.ResourceAttributes {
	return &authorizationv1.ResourceAttributes{
		Namespace:   namespace,
		Verb:        "create",
		Resource:    "pods",
		Subresource: "portforward",
		Name:        podName,
	}
}

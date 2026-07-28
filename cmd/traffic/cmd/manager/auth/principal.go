package auth

import "context"

// Principal is the authenticated Kubernetes identity of a gRPC caller.
type Principal struct {
	Username string
	UID      string
	Groups   []string
	// Extra carries additional authenticator-supplied claims (e.g. the x509
	// authenticator's credential-id) to be forwarded into SubjectAccessReviews.
	Extra map[string][]string
	// PodName and PodUID are the pod-binding claims of a bound (projected)
	// ServiceAccount token. Empty for user tokens.
	PodName string
	PodUID  string
}

// SameAs reports whether p and o represent the same principal: the same username and
// exactly the same UID (two empty UIDs are equal to each other, but an empty UID is
// never equal to a non-empty one).
func (p *Principal) SameAs(o *Principal) bool {
	if p == nil || o == nil {
		return false
	}
	return p.Username == o.Username && p.UID == o.UID
}

type principalKey struct{}

// WithPrincipal returns a context that carries the given Principal.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the Principal carried by ctx, or nil if there is none.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

type authUnavailableKey struct{}

// WithAuthUnavailable marks ctx as carrying a bearer token that could not be
// verified because the TokenReview infrastructure failed. The caller is neither
// authenticated nor known to be an impostor.
func WithAuthUnavailable(ctx context.Context) context.Context {
	return context.WithValue(ctx, authUnavailableKey{}, true)
}

// AuthUnavailable reports whether token verification failed for infrastructure
// reasons. Checks that would deny access for a missing Principal should fail
// with Unavailable rather than PermissionDenied when this is set.
func AuthUnavailable(ctx context.Context) bool {
	v, _ := ctx.Value(authUnavailableKey{}).(bool)
	return v
}

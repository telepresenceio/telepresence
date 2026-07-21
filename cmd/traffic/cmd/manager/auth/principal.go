package auth

import "context"

// Principal is the authenticated Kubernetes identity of a gRPC caller.
type Principal struct {
	Username string
	UID      string
	Groups   []string
	// PodName and PodUID are the pod-binding claims of a bound (projected)
	// ServiceAccount token. Empty for user tokens.
	PodName string
	PodUID  string
}

// SameAs reports whether p and o represent the same principal. Tokens rotate,
// so identity is established by username; UIDs are compared only when both
// are known.
func (p *Principal) SameAs(o *Principal) bool {
	if p == nil || o == nil {
		return false
	}
	if p.Username != o.Username {
		return false
	}
	if p.UID == "" || o.UID == "" {
		return true
	}
	return p.UID == o.UID
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

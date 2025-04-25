package tunnel

import (
	"context"
	"net/netip"
)

type poolKey struct{}

// WithPool returns a context with the given Pool.
func WithPool(ctx context.Context, pool *Pool) context.Context {
	return context.WithValue(ctx, poolKey{}, pool)
}

func GetPool(ctx context.Context) *Pool {
	pool, ok := ctx.Value(poolKey{}).(*Pool)
	if !ok {
		return nil
	}
	return pool
}

type synIpRsvKey struct{}

func WithSyntheticIPResolver(ctx context.Context, resolver SyntheticIPResolver) context.Context {
	return context.WithValue(ctx, synIpRsvKey{}, resolver)
}

type noopSyntheticIPResolver struct{}

func (noopSyntheticIPResolver) Resolve(addr netip.Addr) (netip.Addr, error) {
	return addr, nil
}

func (noopSyntheticIPResolver) ResolveName(netip.Addr) string {
	return ""
}

func GetSyntheticIPResolver(ctx context.Context) SyntheticIPResolver {
	if resolver, ok := ctx.Value(synIpRsvKey{}).(SyntheticIPResolver); ok {
		return resolver
	}
	return noopSyntheticIPResolver{}
}

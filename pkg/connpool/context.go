package connpool

import "context"

type poolKey struct{}

// WithPool returns a context with the given Pool
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

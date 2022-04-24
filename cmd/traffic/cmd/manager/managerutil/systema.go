package managerutil

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/systema"
)

type SystemAPool interface {
	Get() (systema.SystemACRUDClient, error)
	Done() error
}

type systemaPoolKey struct{}

func WithSystemAPool(ctx context.Context, pool SystemAPool) context.Context {
	return context.WithValue(ctx, systemaPoolKey{}, pool)
}

func GetSystemAPool(ctx context.Context) SystemAPool {
	if p, ok := ctx.Value(systemaPoolKey{}).(SystemAPool); ok {
		return p
	}
	return nil
}

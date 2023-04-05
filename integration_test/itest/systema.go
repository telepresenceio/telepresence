package itest

import "context"

type SystemA struct {
	SystemaHost string `json:"systemaHost"`
	SystemaPort int    `json:"systemaPort"`
}

type systemaContextKey struct{}

func WithSystemA(ctx context.Context, sa *SystemA) context.Context {
	return context.WithValue(ctx, systemaContextKey{}, sa)
}

func GetSystemA(ctx context.Context) *SystemA {
	if sa, ok := ctx.Value(systemaContextKey{}).(*SystemA); ok {
		return sa
	}
	return nil
}

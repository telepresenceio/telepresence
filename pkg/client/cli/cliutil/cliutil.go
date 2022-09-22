package cliutil

import (
	"context"
	"regexp"
)

var HostRx = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9\-]*[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9\-]*[a-zA-Z0-9])?)*$`)

type quitting struct{}

func IsQuitting(ctx context.Context) bool {
	if q, ok := ctx.Value(quitting{}).(bool); ok {
		return q
	}
	return false
}

func QuittingContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, quitting{}, true)
}

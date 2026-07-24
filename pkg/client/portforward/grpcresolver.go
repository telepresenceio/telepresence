package portforward

import (
	"context"
	"fmt"

	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"
)

const (
	K8sPFScheme = "k8spf"
)

// resolverBuilder is a grpc resolver.Builder for the k8spf scheme. A target
// is resolved to a pod address exactly once, when the ClientConn is built,
// and the connection stays pinned to that pod for its entire lifetime. When
// the pod goes away, the connection dies with it; establishing a new
// connection, against a fresh resolution, is the caller's responsibility.
type resolverBuilder struct {
	context.Context
}

func NewResolver(ctx context.Context) resolver.Builder {
	return resolverBuilder{Context: ctx}
}

func (p resolverBuilder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (rs resolver.Resolver, err error) {
	if target.URL.Host != "" {
		return nil, fmt.Errorf("invalid (non-empty) authority: %v", target.URL.Host)
	}
	if target.URL.Scheme != K8sPFScheme {
		return nil, fmt.Errorf("invalid scheme: %v", target.URL.Scheme)
	}
	var state resolver.State
	pa, err := resolve(p.Context, target.Endpoint())
	if err == nil {
		state = pa.state()
	} else {
		state = resolver.State{ServiceConfig: &serviceconfig.ParseResult{Err: err}}
	}
	return &noopResolver{}, cc.UpdateState(state)
}

func (p resolverBuilder) Scheme() string {
	return K8sPFScheme
}

type noopResolver struct{}

func (noopResolver) ResolveNow(_ resolver.ResolveNowOptions) {}

func (noopResolver) Close() {}

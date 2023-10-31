package dnet

import "context"

type pfDialerKey struct{}

func WithPortForwardDialer(ctx context.Context, pf PortForwardDialer) context.Context {
	return context.WithValue(ctx, pfDialerKey{}, pf)
}

func GetPortForwardDialer(ctx context.Context) PortForwardDialer {
	if pf, ok := ctx.Value(pfDialerKey{}).(PortForwardDialer); ok {
		return pf
	}
	return nil
}

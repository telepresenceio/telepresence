package tunnel

import (
	"context"
	"net"
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

func WithSyntheticIPResolver(ctx context.Context, r SyntheticIPResolver) context.Context {
	return context.WithValue(ctx, synIpRsvKey{}, r)
}

func GetSyntheticIPResolver(ctx context.Context) SyntheticIPResolver {
	if r, ok := ctx.Value(synIpRsvKey{}).(SyntheticIPResolver); ok {
		return r
	}
	return noopSyntheticIPResolver{}
}

type noopSyntheticIPResolver struct{}

func (noopSyntheticIPResolver) Resolve(addr netip.Addr) (netip.Addr, error) {
	return addr, nil
}

func (noopSyntheticIPResolver) ResolveName(netip.Addr) string {
	return ""
}

type dialerKey struct{}

func WithDialer(ctx context.Context, d Dialer) context.Context {
	return context.WithValue(ctx, dialerKey{}, d)
}

func GetDialer(ctx context.Context) Dialer {
	if d, ok := ctx.Value(dialerKey{}).(Dialer); ok {
		return d
	}
	return DefaultDialer{}
}

type DefaultDialer struct{}

func (n DefaultDialer) DialTCP(ctx context.Context, ap netip.AddrPort) (conn net.Conn, err error) {
	d := net.Dialer{}
	return d.DialContext(ctx, "tcp", ap.String())
}

func (n DefaultDialer) DialUDP(ctx context.Context, localAddr netip.AddrPort, remoteAddr netip.AddrPort) (conn net.Conn, err error) {
	d := net.Dialer{}
	if !localAddr.IsValid() {
		return d.DialContext(ctx, "udp", remoteAddr.String())
	}
	lua := net.UDPAddrFromAddrPort(localAddr)
	rua := net.UDPAddrFromAddrPort(remoteAddr)
	return net.DialUDP("udp", lua, rua)
}

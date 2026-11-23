package remotefs

import (
	"context"
	"net/netip"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// ServeLocal forwards connections made to the local port pp to target, until ctx is done or the
// forwarder fails. On a workstation, only the loopback addresses are bound. A containerized daemon
// binds all addresses of its own network namespace, because the Docker volume plugin and published
// ports reach it through the container's network address.
func ServeLocal(ctx context.Context, pp types.PortAndProto, target netip.AddrPort) error {
	return serveLocal(ctx, pp, target, proc.RunningInContainer())
}

func serveLocal(ctx context.Context, pp types.PortAndProto, target netip.AddrPort, containerized bool) error {
	if containerized {
		return forwarder.New(pp, tunnel.ClientToAgent, target).Serve(ctx, nil)
	}

	// The IPv6 bind is best-effort; the host may have IPv6 disabled.
	go func() {
		f := forwarder.New(pp, tunnel.ClientToAgent, target, forwarder.WithListenAddr(netip.IPv6Loopback()))
		if err := f.Serve(ctx, nil); err != nil && ctx.Err() == nil {
			clog.Debugf(ctx, "IPv6 loopback port-forwarder failed with %v", err)
		}
	}()
	f := forwarder.New(pp, tunnel.ClientToAgent, target, forwarder.WithListenAddr(netip.AddrFrom4([4]byte{127, 0, 0, 1})))
	return f.Serve(ctx, nil)
}

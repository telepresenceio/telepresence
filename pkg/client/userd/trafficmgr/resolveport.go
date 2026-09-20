package trafficmgr

import (
	"context"
	"net/netip"

	dns2 "github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/portforward"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// ResolvePort resolves host and port into a routable address. A numeric port
// only needs the host resolved (an IP literal, or the root daemon's DNS); a
// symbolic port names a service port, which the traffic-manager resolves.
func (s *session) ResolvePort(ctx context.Context, host, port string) (ap types.AddrPortProto, err error) {
	pi := types.PortIdentifier(port)
	if err = pi.Validate(); err != nil {
		return ap, errcat.User.New(err)
	}
	proto, name, num := pi.ProtoAndNameOrNumber()
	if name == "" {
		ip, err := netip.ParseAddr(host)
		if err != nil {
			err = s.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
				r, err := rd.LookupIP(ctx, &daemon.LookupIPRequest{Name: dns2.Fqdn(host)})
				if err != nil {
					return err
				}
				return ip.UnmarshalBinary(r.Ip)
			})
			if err != nil {
				return ap, err
			}
		}
		return types.AddrPortProto{AddrPort: netip.AddrPortFrom(ip, num), Proto: proto}, nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return ap, errcat.User.New("a symbolic port must be used with a service name, not an IP address")
	}
	return resolveServicePort(ctx, s.ManagerClient(), s.SessionInfo(), s.Namespace, host, name, proto)
}

// resolveServicePort resolves a symbolic service port via the traffic-manager,
// falling back to a direct Kubernetes lookup when the manager predates the
// ResolveServicePort RPC.
func resolveServicePort(
	ctx context.Context,
	mc manager.ManagerClient,
	si *manager.SessionInfo,
	namespace, service, port string,
	proto types.Proto,
) (ap types.AddrPortProto, err error) {
	r, err := mc.ResolveServicePort(ctx, &manager.ResolveServicePortRequest{
		Session: si, Namespace: namespace, Service: service, Port: port, Protocol: proto.String(),
	})
	if err == nil {
		var ip netip.Addr
		if err = ip.UnmarshalBinary(r.ClusterIp); err != nil {
			return ap, err
		}
		return types.AddrPortProto{AddrPort: netip.AddrPortFrom(ip, uint16(r.Port)), Proto: proto}, nil
	}
	if status.Code(err) != codes.Unimplemented {
		return ap, err
	}
	if client.GetConfig(ctx).Cluster().UsesExternalManager() {
		return ap, errcat.User.New("symbolic service-port resolution requires a traffic-manager that serves the ResolveServicePort RPC; " +
			"upgrade the traffic-manager or use a numeric port")
	}
	return portforward.ResolveServiceAndPort(ctx, service, namespace, port, proto)
}

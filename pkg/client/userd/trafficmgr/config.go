package trafficmgr

import (
	"context"
	"path/filepath"

	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
)

func (s *session) GetConfig(ctx context.Context) (*client.SessionConfig, error) {
	nc, err := s.rootDaemon.GetNetworkConfig(ctx, &empty.Empty{})
	if err != nil {
		return nil, err
	}
	oi := nc.OutboundInfo
	dns := oi.Dns
	subnets := func(rs []*manager.IPNet) []*iputil.Subnet {
		ss := make([]*iputil.Subnet, len(rs))
		for i, r := range rs {
			ss[i] = (*iputil.Subnet)(iputil.IPNetFromRPC(r))
		}
		return ss
	}
	return &client.SessionConfig{
		ClientFile: filepath.Join(filelocation.AppUserConfigDir(ctx), client.ConfigFile),
		Config:     s.getSessionConfig(),
		DNS: client.DNS{
			LocalIP:         dns.LocalIp,
			RemoteIP:        dns.RemoteIp,
			IncludeSuffixes: dns.IncludeSuffixes,
			ExcludeSuffixes: dns.ExcludeSuffixes,
			LookupTimeout:   dns.LookupTimeout.AsDuration(),
		},
		Routing: client.Routing{
			Subnets:          subnets(nc.Subnets),
			AlsoProxy:        subnets(oi.AlsoProxySubnets),
			NeverProxy:       subnets(oi.NeverProxySubnets),
			AllowConflicting: subnets(oi.AllowConflictingSubnets),
		},
		ManagerNamespace: s.GetManagerNamespace(),
	}, nil
}

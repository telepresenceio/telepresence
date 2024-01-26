package rootd

import (
	"context"

	"github.com/blang/semver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// userdToManagerShortcut overcomes one minor problem, namely that even though a connector.ManagerProxyClient implements a subset
// of the manager.ManagerClient interface, we cannot pass the real thing as the proxy. In the Go implementation, the interface returned
// from a stream function is tightly coupled to the owner of that function and therefore have a different name in the proxy, even though
// its methods are exactly the same. That's why the two affected functions are overridden here, seemingly doing nothing at all. They
// make it possible to pass the manager.ManagerClient as a connector.ManagerProxyClient.
type userdToManagerShortcut struct {
	manager.ManagerClient
}

func (m *userdToManagerShortcut) WatchClusterInfo(ctx context.Context, in *manager.SessionInfo, opts ...grpc.CallOption) (connector.ManagerProxy_WatchClusterInfoClient, error) {
	return m.ManagerClient.WatchClusterInfo(ctx, in, opts...)
}

func (m *userdToManagerShortcut) Tunnel(ctx context.Context, opts ...grpc.CallOption) (connector.ManagerProxy_TunnelClient, error) {
	return m.ManagerClient.Tunnel(ctx, opts...)
}

func (m *userdToManagerShortcut) RealManagerClient() manager.ManagerClient {
	return m.ManagerClient
}

// InProcSession is like Session, but also implements the daemon.DaemonClient interface. This makes it possible to use the session
// in-process from the user daemon, without starting the root daemon gRPC service.
type InProcSession struct {
	*Session
	cancel context.CancelFunc
}

func (rd *InProcSession) Version(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
		Name:       client.DisplayName,
	}, nil
}

func (rd *InProcSession) Status(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*rpc.DaemonStatus, error) {
	nc := rd.getNetworkConfig()
	return &rpc.DaemonStatus{
		Version: &common.VersionInfo{
			ApiVersion: client.APIVersion,
			Version:    client.Version(),
			Name:       client.DisplayName,
		},
		Subnets:        nc.Subnets,
		OutboundConfig: nc.OutboundInfo,
	}, nil
}

func (rd *InProcSession) Quit(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*empty.Empty, error) {
	rd.cancel()
	return &empty.Empty{}, nil
}

func (rd *InProcSession) Connect(ctx context.Context, in *rpc.OutboundInfo, opts ...grpc.CallOption) (*rpc.DaemonStatus, error) {
	return rd.Status(ctx, nil, opts...)
}

func (rd *InProcSession) Disconnect(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*empty.Empty, error) {
	rd.cancel()
	return &empty.Empty{}, nil
}

func (rd *InProcSession) GetNetworkConfig(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*rpc.NetworkConfig, error) {
	return rd.getNetworkConfig(), nil
}

func (rd *InProcSession) SetDnsSearchPath(ctx context.Context, paths *rpc.Paths, opts ...grpc.CallOption) (*empty.Empty, error) {
	rd.SetSearchPath(ctx, paths.Paths, paths.Namespaces)
	return &empty.Empty{}, nil
}

func (rd *InProcSession) SetDNSExcludes(ctx context.Context, in *rpc.SetDNSExcludesRequest, opts ...grpc.CallOption) (*empty.Empty, error) {
	rd.SetExcludes(ctx, in.Excludes)
	return &empty.Empty{}, nil
}

func (rd *InProcSession) SetDNSMappings(ctx context.Context, in *rpc.SetDNSMappingsRequest, opts ...grpc.CallOption) (*empty.Empty, error) {
	rd.SetMappings(ctx, in.Mappings)
	return &empty.Empty{}, nil
}

func (rd *InProcSession) SetLogLevel(ctx context.Context, in *manager.LogLevelRequest, opts ...grpc.CallOption) (*empty.Empty, error) {
	// No loglevel when session runs in the same process as the user daemon.
	return &empty.Empty{}, nil
}

func (rd *InProcSession) WaitForNetwork(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*empty.Empty, error) {
	if err, ok := <-rd.networkReady(ctx); ok {
		return &empty.Empty{}, status.Error(codes.Unavailable, err.Error())
	}
	return &empty.Empty{}, nil
}

func (rd *InProcSession) WaitForAgentIP(ctx context.Context, request *rpc.WaitForAgentIPRequest, opts ...grpc.CallOption) (*empty.Empty, error) {
	return rd.waitForAgentIP(ctx, request)
}

// NewInProcSession returns a root daemon session suitable to use in-process (from the user daemon) and is primarily intended for
// when the user daemon runs in a docker container with NET_ADMIN capabilities.
func NewInProcSession(
	ctx context.Context,
	mi *rpc.OutboundInfo,
	mc manager.ManagerClient,
	ver semver.Version,
) (*InProcSession, error) {
	ctx, cancel := context.WithCancel(ctx)
	session, err := newSession(ctx, mi, &userdToManagerShortcut{mc}, ver)
	if err != nil {
		cancel()
		return nil, err
	}
	return &InProcSession{Session: session, cancel: cancel}, nil
}

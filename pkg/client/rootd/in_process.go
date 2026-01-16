package rootd

import (
	"context"

	"github.com/blang/semver/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// InProcSession is like a session but also implements the daemon.DaemonClient interface. This makes it possible to use the session
// in-process from the user daemon without starting the root daemon gRPC service.
type InProcSession struct {
	*session
}

func (rd *InProcSession) Version(ctx context.Context, _ *empty.Empty, _ ...grpc.CallOption) (*common.VersionInfo, error) {
	return client.VersionInfo(ctx), nil
}

func (rd *InProcSession) Status(ctx context.Context, _ *empty.Empty, _ ...grpc.CallOption) (*rpc.DaemonStatus, error) {
	return &rpc.DaemonStatus{
		Version:        client.VersionInfo(ctx),
		OutboundConfig: rd.getNetworkConfig(),
	}, nil
}

func (rd *InProcSession) Quit(context.Context, *empty.Empty, ...grpc.CallOption) (*rpc.QuitResponse, error) {
	return &rpc.QuitResponse{}, nil
}

func (rd *InProcSession) Connect(ctx context.Context, _ *rpc.NetworkConfig, opts ...grpc.CallOption) (*rpc.DaemonStatus, error) {
	return rd.Status(ctx, nil, opts...)
}

func (rd *InProcSession) Disconnect(context.Context, *empty.Empty, ...grpc.CallOption) (*empty.Empty, error) {
	return &empty.Empty{}, nil
}

func (rd *InProcSession) GetNetworkConfig(context.Context, *empty.Empty, ...grpc.CallOption) (*rpc.NetworkConfig, error) {
	return rd.getNetworkConfig(), nil
}

func (rd *InProcSession) SetDNSTopLevelDomains(_ context.Context, in *rpc.Domains, _ ...grpc.CallOption) (*empty.Empty, error) {
	rd.SetTopLevelDomains(in.Domains)
	return &empty.Empty{}, nil
}

func (rd *InProcSession) SetDNSExcludes(_ context.Context, in *rpc.SetDNSExcludesRequest, _ ...grpc.CallOption) (*empty.Empty, error) {
	rd.SetExcludes(in.Excludes)
	return &empty.Empty{}, nil
}

func (rd *InProcSession) SetDNSMappings(_ context.Context, in *rpc.SetDNSMappingsRequest, _ ...grpc.CallOption) (*empty.Empty, error) {
	rd.SetMappings(in.Mappings)
	return &empty.Empty{}, nil
}

func (rd *InProcSession) SetLogLevel(context.Context, *manager.LogLevelRequest, ...grpc.CallOption) (*empty.Empty, error) {
	// No loglevel when session runs in the same process as the user daemon.
	return &empty.Empty{}, nil
}

func (rd *InProcSession) TranslateEnvIPs(_ context.Context, in *rpc.Environment, _ ...grpc.CallOption) (*rpc.Environment, error) {
	in = rd.translateEnvIPs(in)
	return in, nil
}

func (rd *InProcSession) WaitForNetwork(ctx context.Context, _ *empty.Empty, _ ...grpc.CallOption) (*empty.Empty, error) {
	if err, ok := <-rd.networkReady(ctx); ok {
		return &empty.Empty{}, status.Error(codes.Unavailable, err.Error())
	}
	return &empty.Empty{}, nil
}

func (rd *InProcSession) LookupIP(_ context.Context, request *rpc.LookupIPRequest, _ ...grpc.CallOption) (*rpc.LookupIPResponse, error) {
	return rd.lookupIP(request)
}

func (rd *InProcSession) ResolvePort(ctx context.Context, request *rpc.ResolvePortRequest, _ ...grpc.CallOption) (*rpc.ResolvePortResponse, error) {
	ap, err := rd.resolvePort(ctx, request.Host, request.Port)
	if err != nil {
		return nil, err
	}
	apb, err := ap.MarshalBinary()
	if err != nil {
		return nil, err
	}
	return &rpc.ResolvePortResponse{HostPort: apb}, nil
}

func (rd *InProcSession) RerouteRemotePort(_ context.Context, request *rpc.ReroutePortRequest, _ ...grpc.CallOption) (*empty.Empty, error) {
	var ap types.AddrPortProto
	if err := ap.UnmarshalBinary(request.DstHostPort); err != nil {
		return nil, err
	}
	rd.rerouteRemotePort(ap, uint16(request.SrcPort))
	return &empty.Empty{}, nil
}

func (rd *InProcSession) WaitForAgentIP(ctx context.Context, request *rpc.WaitForAgentIPRequest, _ ...grpc.CallOption) (*rpc.WaitForAgentIPResponse, error) {
	return rd.waitForAgentIP(ctx, request)
}

// NewInProcSession returns a root daemon session suitable to use in-process (from the user daemon) and is primarily intended for
// when the user daemon runs in a docker container with NET_ADMIN capabilities.
func NewInProcSession(
	kc *k8s.Cluster,
	mi *rpc.NetworkConfig,
	mc *grpc.ClientConn,
	ver semver.Version,
	isPodDaemon bool,
) (*InProcSession, error) {
	session, err := newSession(kc, mi, mc, ver, isPodDaemon)
	if err != nil {
		return nil, err
	}
	return &InProcSession{session: session}, nil
}

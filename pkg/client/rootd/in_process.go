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
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// InProcSession is like Session, but also implements the daemon.DaemonClient interface. This makes it possible to use the session
// in-process from the user daemon, without starting the root daemon gRPC service.
type InProcSession struct {
	*session
	cancel context.CancelFunc
}

func (rd *InProcSession) Version(context.Context, *empty.Empty, ...grpc.CallOption) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
		Name:       client.DisplayName,
	}, nil
}

func (rd *InProcSession) Status(ctx context.Context, _ *empty.Empty, _ ...grpc.CallOption) (*rpc.DaemonStatus, error) {
	return &rpc.DaemonStatus{
		Version: &common.VersionInfo{
			ApiVersion: client.APIVersion,
			Version:    client.Version(),
			Name:       client.DisplayName,
		},
		OutboundConfig: rd.getNetworkConfig(ctx),
	}, nil
}

func (rd *InProcSession) Quit(context.Context, *empty.Empty, ...grpc.CallOption) (*empty.Empty, error) {
	rd.cancel()
	return &empty.Empty{}, nil
}

func (rd *InProcSession) Connect(ctx context.Context, _ *rpc.NetworkConfig, opts ...grpc.CallOption) (*rpc.DaemonStatus, error) {
	return rd.Status(ctx, nil, opts...)
}

func (rd *InProcSession) Disconnect(context.Context, *empty.Empty, ...grpc.CallOption) (*empty.Empty, error) {
	rd.cancel()
	return &empty.Empty{}, nil
}

func (rd *InProcSession) GetNetworkConfig(ctx context.Context, _ *empty.Empty, _ ...grpc.CallOption) (*rpc.NetworkConfig, error) {
	return rd.getNetworkConfig(ctx), nil
}

func (rd *InProcSession) SetDNSTopLevelDomains(ctx context.Context, in *rpc.Domains, _ ...grpc.CallOption) (*empty.Empty, error) {
	rd.SetTopLevelDomains(ctx, in.Domains)
	return &empty.Empty{}, nil
}

func (rd *InProcSession) SetDNSExcludes(ctx context.Context, in *rpc.SetDNSExcludesRequest, _ ...grpc.CallOption) (*empty.Empty, error) {
	rd.SetExcludes(ctx, in.Excludes)
	return &empty.Empty{}, nil
}

func (rd *InProcSession) SetDNSMappings(ctx context.Context, in *rpc.SetDNSMappingsRequest, _ ...grpc.CallOption) (*empty.Empty, error) {
	rd.SetMappings(ctx, in.Mappings)
	return &empty.Empty{}, nil
}

func (rd *InProcSession) SetLogLevel(context.Context, *manager.LogLevelRequest, ...grpc.CallOption) (*empty.Empty, error) {
	// No loglevel when session runs in the same process as the user daemon.
	return &empty.Empty{}, nil
}

func (rd *InProcSession) TranslateEnvIPs(ctx context.Context, in *rpc.Environment, opts ...grpc.CallOption) (*rpc.Environment, error) {
	in = rd.translateEnvIPs(ctx, in)
	return in, nil
}

func (rd *InProcSession) WaitForNetwork(ctx context.Context, _ *empty.Empty, _ ...grpc.CallOption) (*empty.Empty, error) {
	if err, ok := <-rd.networkReady(ctx); ok {
		return &empty.Empty{}, status.Error(codes.Unavailable, err.Error())
	}
	return &empty.Empty{}, nil
}

func (rd *InProcSession) LookupIP(ctx context.Context, request *rpc.LookupIPRequest, _ ...grpc.CallOption) (*rpc.LookupIPResponse, error) {
	return rd.lookupIP(ctx, request)
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

func (rd *InProcSession) RerouteRemotePort(ctx context.Context, request *rpc.ReroutePortRequest, _ ...grpc.CallOption) (*empty.Empty, error) {
	var ap types.AddrPortProto
	if err := ap.UnmarshalBinary(request.DstHostPort); err != nil {
		return nil, err
	}
	rd.rerouteRemotePort(ctx, ap, uint16(request.SrcPort))
	return &empty.Empty{}, nil
}

func (rd *InProcSession) WaitForAgentIP(ctx context.Context, request *rpc.WaitForAgentIPRequest, _ ...grpc.CallOption) (*rpc.WaitForAgentIPResponse, error) {
	return rd.waitForAgentIP(ctx, request)
}

// NewInProcSession returns a root daemon session suitable to use in-process (from the user daemon) and is primarily intended for
// when the user daemon runs in a docker container with NET_ADMIN capabilities.
func NewInProcSession(
	ctx context.Context,
	mi *rpc.NetworkConfig,
	mc *grpc.ClientConn,
	ver semver.Version,
	isPodDaemon bool,
) (context.Context, *InProcSession, error) {
	ctx, cancel := context.WithCancel(ctx)
	ctx, session, err := newSession(ctx, mi, mc, ver, isPodDaemon)
	if err != nil {
		cancel()
		return ctx, nil, err
	}
	return ctx, &InProcSession{session: session, cancel: cancel}, nil
}

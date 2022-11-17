package daemon

import (
	"context"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	managerrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// mgrProxy implements rpc.ManagerServer, but just proxies all requests through a rpc.ManagerClient.
type mgrProxy struct {
	sync.RWMutex
	clientX      managerrpc.ManagerClient
	callOptionsX []grpc.CallOption

	managerrpc.UnsafeManagerServer
}

var _ managerrpc.ManagerServer = &mgrProxy{}

func (p *mgrProxy) setClient(client managerrpc.ManagerClient, callOptions ...grpc.CallOption) {
	p.Lock()
	p.clientX = client
	p.callOptionsX = callOptions
	p.Unlock()
}

func (p *mgrProxy) get() (managerrpc.ManagerClient, []grpc.CallOption, error) {
	p.RLock()
	defer p.RUnlock()
	if p.clientX == nil {
		return nil, nil, status.Error(codes.Unavailable, "telepresence: the userd is not connected to the manager")
	}
	return p.clientX, p.callOptionsX, nil
}

func (p *mgrProxy) GetIntercept(ctx context.Context, arg *managerrpc.GetInterceptRequest) (*managerrpc.InterceptInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.GetIntercept(ctx, arg, callOptions...)
}

func (p *mgrProxy) Version(ctx context.Context, arg *empty.Empty) (*managerrpc.VersionInfo2, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Version(ctx, arg, callOptions...)
}

func (p *mgrProxy) GetLicense(ctx context.Context, arg *empty.Empty) (*managerrpc.License, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.GetLicense(ctx, arg, callOptions...)
}

func (p *mgrProxy) GetTelepresenceAPI(ctx context.Context, arg *empty.Empty) (*managerrpc.TelepresenceAPIInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.GetTelepresenceAPI(ctx, arg, callOptions...)
}

func (p *mgrProxy) CanConnectAmbassadorCloud(ctx context.Context, arg *empty.Empty) (*managerrpc.AmbassadorCloudConnection, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.CanConnectAmbassadorCloud(ctx, arg, callOptions...)
}

func (p *mgrProxy) GetCloudConfig(ctx context.Context, arg *empty.Empty) (*managerrpc.AmbassadorCloudConfig, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	// TODO (dyung): We might want to make this always return an error since the client should already have the config.
	return client.GetCloudConfig(ctx, arg, callOptions...)
}

func (p *mgrProxy) GetClientConfig(ctx context.Context, arg *empty.Empty) (*managerrpc.CLIConfig, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.GetClientConfig(ctx, arg, callOptions...)
}

func (p *mgrProxy) ArriveAsClient(ctx context.Context, arg *managerrpc.ClientInfo) (*managerrpc.SessionInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.ArriveAsClient(ctx, arg, callOptions...)
}

func (p *mgrProxy) ArriveAsAgent(ctx context.Context, arg *managerrpc.AgentInfo) (*managerrpc.SessionInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.ArriveAsAgent(ctx, arg, callOptions...)
}

func (p *mgrProxy) Remain(ctx context.Context, arg *managerrpc.RemainRequest) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Remain(ctx, arg, callOptions...)
}

func (p *mgrProxy) Depart(ctx context.Context, arg *managerrpc.SessionInfo) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Depart(ctx, arg, callOptions...)
}

func (p *mgrProxy) WatchAgents(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchAgentsServer) error {
	return status.Error(codes.Unimplemented, "WatchAgents was deprecated in 2.5.5")
}

func (p *mgrProxy) WatchAgentsNS(arg *managerrpc.AgentsRequest, srv managerrpc.Manager_WatchAgentsNSServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	cli, err := client.WatchAgentsNS(srv.Context(), arg, callOptions...)
	if err != nil {
		return err
	}
	for {
		snapshot, err := cli.Recv()
		if err != nil {
			if err == io.EOF || srv.Context().Err() != nil {
				return nil
			}
			return err
		}
		if err = srv.Send(snapshot); err != nil {
			return err
		}
	}
}

func (p *mgrProxy) WatchIntercepts(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchInterceptsServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	cli, err := client.WatchIntercepts(srv.Context(), arg, callOptions...)
	if err != nil {
		return err
	}
	for {
		snapshot, err := cli.Recv()
		if err != nil {
			if err == io.EOF || srv.Context().Err() != nil {
				return nil
			}
			return err
		}
		if err = srv.Send(snapshot); err != nil {
			return err
		}
	}
}

func (p *mgrProxy) PrepareIntercept(_ context.Context, _ *managerrpc.CreateInterceptRequest) (*managerrpc.PreparedIntercept, error) {
	return nil, errors.New("must call connector.CanIntercept instead of manager.CreateIntercept")
}

func (p *mgrProxy) CreateIntercept(_ context.Context, _ *managerrpc.CreateInterceptRequest) (*managerrpc.InterceptInfo, error) {
	return nil, errors.New("must call connector.CreateIntercept instead of manager.CreateIntercept")
}

func (p *mgrProxy) RemoveIntercept(context.Context, *managerrpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "must call connector.RemoveIntercept instead of manager.RemoveIntercept")
}

func (p *mgrProxy) UpdateIntercept(ctx context.Context, arg *managerrpc.UpdateInterceptRequest) (*managerrpc.InterceptInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.UpdateIntercept(ctx, arg, callOptions...)
}

func (p *mgrProxy) ReviewIntercept(ctx context.Context, arg *managerrpc.ReviewInterceptRequest) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.ReviewIntercept(ctx, arg, callOptions...)
}

func (p *mgrProxy) ClientTunnel(managerrpc.Manager_ClientTunnelServer) error {
	return status.Error(codes.Unimplemented, "ClientTunnel was deprecated in 2.4.5 and has since been removed")
}

func (p *mgrProxy) AgentTunnel(managerrpc.Manager_AgentTunnelServer) error {
	return status.Error(codes.Unimplemented, "AgentTunnel was deprecated in 2.4.5 and has since been removed")
}

func (p *mgrProxy) Tunnel(managerrpc.Manager_TunnelServer) error {
	return status.Error(codes.Unimplemented, "Tunnel must be called from the root daemon")
}

func (p *mgrProxy) WatchDial(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchDialServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	cli, err := client.WatchDial(srv.Context(), arg, callOptions...)
	if err != nil {
		return err
	}
	for {
		request, err := cli.Recv()
		if err != nil {
			if err == io.EOF || srv.Context().Err() != nil {
				return nil
			}
			return err
		}
		if err = srv.Send(request); err != nil {
			return err
		}
	}
}

// LookupHost
// Deprecated: Use LookupDNS
//
//nolint:staticcheck // retained for backward compatibility
func (p *mgrProxy) LookupHost(ctx context.Context, arg *managerrpc.LookupHostRequest) (*managerrpc.LookupHostResponse, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.LookupHost(ctx, arg, callOptions...)
}

// AgentLookupHostResponse
// Deprecated: Use AgentLookupDNSResponse
//
//nolint:staticcheck // retained for backward compatibility
func (p *mgrProxy) AgentLookupHostResponse(context.Context, *managerrpc.LookupHostAgentResponse) (*empty.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "must call manager.AgentLookupHostResponse from an agent (intercepted Pod), not from a client (workstation)")
}

func (p *mgrProxy) WatchLookupHost(*managerrpc.SessionInfo, managerrpc.Manager_WatchLookupHostServer) error {
	return status.Error(codes.Unimplemented, "must call manager.WatchLookupHost from an agent (intercepted Pod), not from a client (workstation)")
}

func (p *mgrProxy) LookupDNS(ctx context.Context, arg *managerrpc.DNSRequest) (*managerrpc.DNSResponse, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.LookupDNS(ctx, arg, callOptions...)
}

func (p *mgrProxy) AgentLookupDNSResponse(context.Context, *managerrpc.DNSAgentResponse) (*empty.Empty, error) {
	return nil, status.Error(codes.Unimplemented, "must call manager.AgentLookupDNSResponse from an agent (intercepted Pod), not from a client (workstation)")
}

func (p *mgrProxy) WatchLookupDNS(*managerrpc.SessionInfo, managerrpc.Manager_WatchLookupDNSServer) error {
	return status.Error(codes.Unimplemented, "must call manager.WatchLookupDNS from an agent (intercepted Pod), not from a client (workstation)")
}

func (p *mgrProxy) WatchClusterInfo(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchClusterInfoServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	cli, err := client.WatchClusterInfo(srv.Context(), arg, callOptions...)
	if err != nil {
		return err
	}
	for {
		info, err := cli.Recv()
		if err != nil {
			if err == io.EOF || srv.Context().Err() != nil {
				return nil
			}
			return err
		}
		if err = srv.Send(info); err != nil {
			return err
		}
	}
}

func (p *mgrProxy) SetLogLevel(ctx context.Context, request *managerrpc.LogLevelRequest) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.SetLogLevel(ctx, request, callOptions...)
}

func (p *mgrProxy) GetLogs(context.Context, *managerrpc.GetLogsRequest) (*managerrpc.LogsResponse, error) {
	return nil, status.Error(codes.Unimplemented, " \"must call connector.GatherLogs instead of manager.GetLogs\"")
}

func (p *mgrProxy) WatchLogLevel(*empty.Empty, managerrpc.Manager_WatchLogLevelServer) error {
	return status.Error(codes.Unimplemented, "must call manager.WatchLogLevel from an agent (intercepted Pod), not from a client (workstation)")
}

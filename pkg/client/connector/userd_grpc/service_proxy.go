package userd_grpc

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	managerrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// mgrProxy implements rpc.ManagerServer, but just proxies all requests through a rpc.ManagerClient.
type mgrProxy struct {
	client      managerrpc.ManagerClient
	callOptions []grpc.CallOption

	managerrpc.UnsafeManagerServer
}

// NewManagerProxy returns a rpc.ManagerServer that just proxies all requests through the given rpc.ManagerClient.
func NewManagerProxy(client managerrpc.ManagerClient, callOptions ...grpc.CallOption) managerrpc.ManagerServer {
	return &mgrProxy{
		client:      client,
		callOptions: callOptions,
	}
}
func (p *mgrProxy) GetIntercept(ctx context.Context, arg *managerrpc.GetInterceptRequest) (*managerrpc.InterceptInfo, error) {
	return p.client.GetIntercept(ctx, arg, p.callOptions...)
}

func (p *mgrProxy) Version(ctx context.Context, arg *empty.Empty) (*managerrpc.VersionInfo2, error) {
	return p.client.Version(ctx, arg, p.callOptions...)
}
func (p *mgrProxy) GetLicense(ctx context.Context, arg *empty.Empty) (*managerrpc.License, error) {
	return p.client.GetLicense(ctx, arg, p.callOptions...)
}

func (p *mgrProxy) CanConnectAmbassadorCloud(ctx context.Context, arg *empty.Empty) (*managerrpc.AmbassadorCloudConnection, error) {
	return p.client.CanConnectAmbassadorCloud(ctx, arg, p.callOptions...)
}

func (p *mgrProxy) GetCloudConfig(ctx context.Context, arg *empty.Empty) (*managerrpc.AmbassadorCloudConfig, error) {
	// TODO (dyung): We might want to make this always return an error since the
	// client should already have the config.
	return p.client.GetCloudConfig(ctx, arg, p.callOptions...)
}

func (p *mgrProxy) ArriveAsClient(ctx context.Context, arg *managerrpc.ClientInfo) (*managerrpc.SessionInfo, error) {
	return p.client.ArriveAsClient(ctx, arg, p.callOptions...)
}
func (p *mgrProxy) ArriveAsAgent(ctx context.Context, arg *managerrpc.AgentInfo) (*managerrpc.SessionInfo, error) {
	return p.client.ArriveAsAgent(ctx, arg, p.callOptions...)
}
func (p *mgrProxy) Remain(ctx context.Context, arg *managerrpc.RemainRequest) (*empty.Empty, error) {
	return p.client.Remain(ctx, arg, p.callOptions...)
}
func (p *mgrProxy) Depart(ctx context.Context, arg *managerrpc.SessionInfo) (*empty.Empty, error) {
	return p.client.Depart(ctx, arg, p.callOptions...)
}

func (p *mgrProxy) WatchAgents(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchAgentsServer) error {
	cli, err := p.client.WatchAgents(srv.Context(), arg, p.callOptions...)
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
	cli, err := p.client.WatchIntercepts(srv.Context(), arg, p.callOptions...)
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

func (p *mgrProxy) CreateIntercept(ctx context.Context, arg *managerrpc.CreateInterceptRequest) (*managerrpc.InterceptInfo, error) {
	return nil, errors.New("must call connector.CreateIntercept instead of manager.CreateIntercept")
}
func (p *mgrProxy) RemoveIntercept(ctx context.Context, arg *managerrpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	return nil, errors.New("must call connector.RemoveIntercept instead of manager.RemoveIntercept")
}
func (p *mgrProxy) UpdateIntercept(ctx context.Context, arg *managerrpc.UpdateInterceptRequest) (*managerrpc.InterceptInfo, error) {
	return p.client.UpdateIntercept(ctx, arg, p.callOptions...)
}
func (p *mgrProxy) ReviewIntercept(ctx context.Context, arg *managerrpc.ReviewInterceptRequest) (*empty.Empty, error) {
	return p.client.ReviewIntercept(ctx, arg, p.callOptions...)
}

func (p *mgrProxy) ClientTunnel(fhDaemon managerrpc.Manager_ClientTunnelServer) error {
	ctx := fhDaemon.Context()
	fhManager, err := p.client.ClientTunnel(ctx, p.callOptions...)
	if err != nil {
		return err
	}
	grp := dgroup.NewGroup(ctx, dgroup.GroupConfig{})
	grp.Go("manager->daemon", func(ctx context.Context) error {
		for {
			payload, err := fhManager.Recv()
			if err != nil {
				if err == io.EOF || ctx.Err() != nil {
					return nil
				}
				return err
			}
			if err = fhDaemon.Send(payload); err != nil {
				return err
			}
		}
	})
	grp.Go("daemon->manager", func(ctx context.Context) error {
		for {
			payload, err := fhDaemon.Recv()
			if err != nil {
				if err == io.EOF || ctx.Err() != nil {
					return nil
				}
				return err
			}
			if err = fhManager.Send(payload); err != nil {
				return err
			}
		}
	})
	return grp.Wait()
}

func (p *mgrProxy) AgentTunnel(server managerrpc.Manager_AgentTunnelServer) error {
	return errors.New("must call manager.AgentTunnel from an agent (intercepted Pod), not from a client (workstation)")
}

func (p *mgrProxy) LookupHost(ctx context.Context, arg *managerrpc.LookupHostRequest) (*managerrpc.LookupHostResponse, error) {
	return p.client.LookupHost(ctx, arg, p.callOptions...)
}

func (p *mgrProxy) AgentLookupHostResponse(ctx context.Context, arg *managerrpc.LookupHostAgentResponse) (*empty.Empty, error) {
	return p.client.AgentLookupHostResponse(ctx, arg, p.callOptions...)
}

func (p *mgrProxy) WatchLookupHost(_ *managerrpc.SessionInfo, server managerrpc.Manager_WatchLookupHostServer) error {
	return errors.New("must call manager.WatchLookupHost from an agent (intercepted Pod), not from a client (workstation)")
}

func (p *mgrProxy) WatchClusterInfo(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchClusterInfoServer) error {
	cli, err := p.client.WatchClusterInfo(srv.Context(), arg, p.callOptions...)
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
	return p.client.SetLogLevel(ctx, request, p.callOptions...)
}

func (p *mgrProxy) GetLogs(ctx context.Context, request *managerrpc.GetLogsRequest) (*managerrpc.LogsResponse, error) {
	return p.client.GetLogs(ctx, request, p.callOptions...)
}

func (p *mgrProxy) WatchLogLevel(e *empty.Empty, server managerrpc.Manager_WatchLogLevelServer) error {
	return errors.New("must call manager.WatchLogLevel from an agent (intercepted Pod), not from a client (workstation)")
}

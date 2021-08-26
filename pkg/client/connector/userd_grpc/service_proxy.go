package userd_grpc

import (
	"context"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dgroup"
	managerrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// MgrProxy implements rpc.ManagerServer, but just proxies all requests through a rpc.ManagerClient.
// Use the `SetClient` method safely adjust the client at runtime.  MgrProxy does not need
// initialized; the zero value works fine.
type MgrProxy struct {
	mu          sync.RWMutex
	client      managerrpc.ManagerClient
	callOptions []grpc.CallOption

	managerrpc.UnsafeManagerServer
}

func (p *MgrProxy) SetClient(client managerrpc.ManagerClient, callOptions ...grpc.CallOption) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.client, p.callOptions = client, callOptions
}

func (p *MgrProxy) get() (managerrpc.ManagerClient, []grpc.CallOption, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil, nil, grpcstatus.Error(grpccodes.FailedPrecondition,
			"telepresence: the userd is not connected to the manager")
	}
	return p.client, p.callOptions, nil
}

func (p *MgrProxy) Version(ctx context.Context, arg *empty.Empty) (*managerrpc.VersionInfo2, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Version(ctx, arg, callOptions...)
}
func (p *MgrProxy) GetLicense(ctx context.Context, arg *empty.Empty) (*managerrpc.License, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.GetLicense(ctx, arg, callOptions...)
}

func (p *MgrProxy) CanConnectAmbassadorCloud(ctx context.Context, arg *empty.Empty) (*managerrpc.AmbassadorCloudConnection, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.CanConnectAmbassadorCloud(ctx, arg, callOptions...)
}

func (p *MgrProxy) GetCloudConfig(ctx context.Context, arg *empty.Empty) (*managerrpc.AmbassadorCloudConfig, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	// TODO (dyung): We might want to make this always return an error since the
	// client should already have the config.
	return client.GetCloudConfig(ctx, arg, callOptions...)
}

func (p *MgrProxy) ArriveAsClient(ctx context.Context, arg *managerrpc.ClientInfo) (*managerrpc.SessionInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.ArriveAsClient(ctx, arg, callOptions...)
}
func (p *MgrProxy) ArriveAsAgent(ctx context.Context, arg *managerrpc.AgentInfo) (*managerrpc.SessionInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.ArriveAsAgent(ctx, arg, callOptions...)
}
func (p *MgrProxy) Remain(ctx context.Context, arg *managerrpc.RemainRequest) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Remain(ctx, arg, callOptions...)
}
func (p *MgrProxy) Depart(ctx context.Context, arg *managerrpc.SessionInfo) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Depart(ctx, arg, callOptions...)
}

func (p *MgrProxy) WatchAgents(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchAgentsServer) error {
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}
	cli, err := client.WatchAgents(srv.Context(), arg, callOptions...)
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
		if err := srv.Send(snapshot); err != nil {
			return err
		}
	}
}
func (p *MgrProxy) WatchIntercepts(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchInterceptsServer) error {
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
		if err := srv.Send(snapshot); err != nil {
			return err
		}
	}
}
func (p *MgrProxy) CreateIntercept(ctx context.Context, arg *managerrpc.CreateInterceptRequest) (*managerrpc.InterceptInfo, error) {
	return nil, errors.New("must call connector.CreateIntercept instead of manager.CreateIntercept")
}
func (p *MgrProxy) RemoveIntercept(ctx context.Context, arg *managerrpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	return nil, errors.New("must call connector.RemoveIntercept instead of manager.RemoveIntercept")
}
func (p *MgrProxy) UpdateIntercept(ctx context.Context, arg *managerrpc.UpdateInterceptRequest) (*managerrpc.InterceptInfo, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.UpdateIntercept(ctx, arg, callOptions...)
}
func (p *MgrProxy) ReviewIntercept(ctx context.Context, arg *managerrpc.ReviewInterceptRequest) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.ReviewIntercept(ctx, arg, callOptions...)
}

func (p *MgrProxy) ClientTunnel(fhDaemon managerrpc.Manager_ClientTunnelServer) error {
	ctx := fhDaemon.Context()
	client, callOptions, err := p.get()
	if err != nil {
		return err
	}

	fhManager, err := client.ClientTunnel(ctx, callOptions...)
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
			if err := fhDaemon.Send(payload); err != nil {
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
			if err := fhManager.Send(payload); err != nil {
				return err
			}
		}
	})
	return grp.Wait()
}

func (p *MgrProxy) AgentTunnel(server managerrpc.Manager_AgentTunnelServer) error {
	return errors.New("must call manager.AgentTunnel from an agent (intercepted Pod), not from a client (workstation)")
}

func (p *MgrProxy) LookupHost(ctx context.Context, arg *managerrpc.LookupHostRequest) (*managerrpc.LookupHostResponse, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.LookupHost(ctx, arg, callOptions...)
}

func (p *MgrProxy) AgentLookupHostResponse(ctx context.Context, arg *managerrpc.LookupHostAgentResponse) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.AgentLookupHostResponse(ctx, arg, callOptions...)
}

func (p *MgrProxy) WatchLookupHost(_ *managerrpc.SessionInfo, server managerrpc.Manager_WatchLookupHostServer) error {
	return errors.New("must call manager.WatchLookupHost from an agent (intercepted Pod), not from a client (workstation)")
}

func (p *MgrProxy) WatchClusterInfo(arg *managerrpc.SessionInfo, srv managerrpc.Manager_WatchClusterInfoServer) error {
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
		if err := srv.Send(info); err != nil {
			return err
		}
	}
}

func (p *MgrProxy) SetLogLevel(ctx context.Context, request *managerrpc.LogLevelRequest) (*empty.Empty, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.SetLogLevel(ctx, request, callOptions...)
}

func (p *MgrProxy) WatchLogLevel(e *empty.Empty, server managerrpc.Manager_WatchLogLevelServer) error {
	return errors.New("must call manager.WatchLogLevel from an agent (intercepted Pod), not from a client (workstation)")
}

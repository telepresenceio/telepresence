package connector

import (
	"context"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc"
	grpccodes "google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	managerrpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// mgrProxy implements rpc.ManagerServer, but just proxies all requests through a rpc.ManagerClient.
// Use the `SetClient` method safely adjust the client at runtime.
type mgrProxy struct {
	mu          sync.RWMutex
	client      managerrpc.ManagerClient
	callOptions []grpc.CallOption

	managerrpc.UnsafeManagerServer
}

var _ managerrpc.ManagerServer = (*mgrProxy)(nil)

func (p *mgrProxy) SetClient(client managerrpc.ManagerClient, callOptions ...grpc.CallOption) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.client, p.callOptions = client, callOptions
}

func (p *mgrProxy) get() (managerrpc.ManagerClient, []grpc.CallOption, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.client == nil {
		return nil, nil, grpcstatus.Error(grpccodes.FailedPrecondition,
			"telepresence: the userd is not connected to the manager")
	}
	return p.client, p.callOptions, nil
}

func (p *mgrProxy) Version(ctx context.Context, arg *empty.Empty) (*managerrpc.VersionInfo2, error) {
	client, callOptions, err := p.get()
	if err != nil {
		return nil, err
	}
	return client.Version(ctx, arg, callOptions...)
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
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := srv.Send(snapshot); err != nil {
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
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := srv.Send(snapshot); err != nil {
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

package tunnel

import (
	"context"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

type Client interface {
	Send(*rpc.TunnelMessage) error
	Recv() (*rpc.TunnelMessage, error)
	grpc.ClientStream
}

type Provider interface {
	Tunnel(ctx context.Context, opts ...grpc.CallOption) (Client, error)
}

type mgrProvider struct {
	rpc.ManagerClient
}

func (m mgrProvider) Tunnel(ctx context.Context, opts ...grpc.CallOption) (Client, error) {
	return m.ManagerClient.Tunnel(ctx, opts...)
}

func ManagerProvider(m rpc.ManagerClient) Provider {
	return mgrProvider{m}
}

type agentProvider struct {
	agent.AgentClient
}

func (m agentProvider) Tunnel(ctx context.Context, opts ...grpc.CallOption) (Client, error) {
	return m.AgentClient.Tunnel(ctx, opts...)
}

func AgentProvider(a agent.AgentClient) Provider {
	return agentProvider{a}
}

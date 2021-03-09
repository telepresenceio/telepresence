package actions

import (
	"context"

	"google.golang.org/grpc"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func ListAllAgents(ctx context.Context, client rpc.ManagerClient, sessionID string, opts ...grpc.CallOption) ([]*rpc.AgentInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.WatchAgents(ctx, &rpc.SessionInfo{SessionId: sessionID}, opts...)
	if err != nil {
		return nil, err
	}
	snapshot, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	return snapshot.Agents, nil
}

func listIntercepts(ctx context.Context, client rpc.ManagerClient, sessionID string, opts ...grpc.CallOption) ([]*rpc.InterceptInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.WatchIntercepts(ctx, &rpc.SessionInfo{SessionId: sessionID}, opts...)
	if err != nil {
		return nil, err
	}
	snapshot, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	return snapshot.Intercepts, nil
}

func ListAllIntercepts(ctx context.Context, client rpc.ManagerClient, opts ...grpc.CallOption) ([]*rpc.InterceptInfo, error) {
	return listIntercepts(ctx, client, "", opts...)
}

func ListMyIntercepts(ctx context.Context, client rpc.ManagerClient, sessionID string, opts ...grpc.CallOption) ([]*rpc.InterceptInfo, error) {
	return listIntercepts(ctx, client, sessionID, opts...)
}

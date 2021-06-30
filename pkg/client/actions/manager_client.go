package actions

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func ListAllAgents(ctx context.Context, client rpc.ManagerClient, sessionID string, opts ...grpc.CallOption) ([]*rpc.AgentInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := client.WatchAgents(ctx, &rpc.SessionInfo{SessionId: sessionID}, opts...)
	if err != nil {
		return nil, fmt.Errorf("manager.WatchAgents dial: %w", err)
	}
	snapshot, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("manager.WatchAgents recv: %w", err)
	}
	return snapshot.Agents, nil
}

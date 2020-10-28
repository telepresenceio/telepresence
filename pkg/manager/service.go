package manager

import (
	"context"

	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/ambassador/pkg/dlog"
	rpc "github.com/datawire/telepresence2/pkg/rpc/manager"
)

type Manager struct {
	rpc.UnimplementedManagerServer
}

func NewManager(context.Context) *Manager {
	return &Manager{}
}

func (*Manager) Version(context.Context, *empty.Empty) (*rpc.VersionInfo2, error) {
	return &rpc.VersionInfo2{Version: "VERSION"}, nil
}

func validateClient(client *rpc.ClientInfo) string {
	switch {
	case client.Name == "":
		return "name must not be empty"
	case client.InstallId == "":
		return "install ID must not be empty"
	case client.Product == "":
		return "product must not be empty"
	case client.Version == "":
		return "version must not be empty"
	}

	return ""
}

func (m *Manager) ArriveAsClient(ctx context.Context, client *rpc.ClientInfo) (*rpc.SessionInfo, error) {
	dlog.Debug(ctx, "ArriveAsClient called")

	if val := validateClient(client); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	sessionID := "client-session-id"

	return &rpc.SessionInfo{SessionId: sessionID}, nil
}

func (m *Manager) ArriveAsAgent(ctx context.Context, _ *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ArriveAsAgent not implemented")
}

func (m *Manager) Remain(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	dlog.Debug(ctx, "Remain called")

	return &empty.Empty{}, nil
}

func (m *Manager) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	dlog.Debug(ctx, "Depart called")

	return &empty.Empty{}, nil
}

// FIXME Unimplemented
func (m *Manager) WatchAgents(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentsServer) error {
	ctx := stream.Context()
	dlog.Debug(ctx, "WatchAgents called")

	res := &rpc.AgentInfoSnapshot{Agents: []*rpc.AgentInfo{}}

	if err := stream.Send(res); err != nil {
		return err
	}

	<-stream.Context().Done()
	return nil
}

// FIXME Unimplemented
func (m *Manager) WatchIntercepts(_ *rpc.SessionInfo, stream rpc.Manager_WatchInterceptsServer) error {
	ctx := stream.Context()
	dlog.Debug(ctx, "WatchIntercepts called")

	res := &rpc.InterceptInfoSnapshot{Intercepts: []*rpc.InterceptInfo{}}

	if err := stream.Send(res); err != nil {
		return err
	}

	<-stream.Context().Done()
	return nil
}

func (m *Manager) CreateIntercept(context.Context, *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	return nil, status.Errorf(codes.Unimplemented, "method CreateIntercept not implemented")
}

func (m *Manager) RemoveIntercept(context.Context, *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RemoveIntercept not implemented")
}

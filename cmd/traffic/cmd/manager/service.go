package manager

import (
	"context"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/datawire/telepresence2/pkg/rpc/manager"
	"github.com/datawire/telepresence2/pkg/version"
)

type Manager struct {
	rpc.UnimplementedManagerServer

	state *State
}

type wall struct{}

func (wall) Now() time.Time {
	return time.Now()
}

func NewManager(ctx context.Context) *Manager {
	return &Manager{state: NewState(ctx, wall{})}
}

// Version returns the version information of the Manager.
func (*Manager) Version(context.Context, *empty.Empty) (*rpc.VersionInfo2, error) {
	return &rpc.VersionInfo2{Version: version.Version}, nil
}

// ArriveAsClient establishes a session between a client and the Manager.
func (m *Manager) ArriveAsClient(ctx context.Context, client *rpc.ClientInfo) (*rpc.SessionInfo, error) {
	dlog.Debug(ctx, "ArriveAsClient called")

	if val := validateClient(client); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	sessionID := m.state.AddClient(client)

	return &rpc.SessionInfo{SessionId: sessionID}, nil
}

// ArriveAsAgent establishes a session between an agent and the Manager.
func (m *Manager) ArriveAsAgent(ctx context.Context, agent *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	dlog.Debug(ctx, "ArriveAsAgent called")

	if val := validateAgent(agent); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	sessionID := m.state.AddAgent(agent)

	return &rpc.SessionInfo{SessionId: sessionID}, nil
}

// Remain indicates that the session is still valid.
func (m *Manager) Remain(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	dlog.Debug(ctx, "Remain called")

	if ok := m.state.Mark(session.SessionId); !ok {
		return nil, status.Errorf(codes.NotFound, "Session %q not found", session.SessionId)
	}

	return &empty.Empty{}, nil
}

// Depart terminates a session.
func (m *Manager) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	dlog.Debug(ctx, "Depart called")

	m.state.Remove(session.SessionId)

	return &empty.Empty{}, nil
}

// WatchAgents notifies a client of the set of known Agents.
func (m *Manager) WatchAgents(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentsServer) error {
	ctx := stream.Context()
	sessionID := session.SessionId

	dlog.Debugf(ctx, "WatchAgents called (sessionID=%q)", sessionID)

	if !m.state.HasClient(sessionID) {
		return status.Errorf(codes.NotFound, "Client session %q not found", session.SessionId)
	}

	entry := m.state.Get(sessionID)
	sessionCtx := entry.Context()
	changed := m.state.WatchAgents(sessionID)

	for {
		// FIXME This will loop over the presence list looking for agents for
		// every single watcher. How inefficient!
		res := &rpc.AgentInfoSnapshot{Agents: m.state.GetAgents()}

		if err := stream.Send(res); err != nil {
			return err
		}

		select {
		case <-changed:
			// It's time to send another message. Loop.
		case <-ctx.Done():
			// Manager is shutting down.
			return nil
		case <-sessionCtx.Done():
			// Manager believes this session has ended.
			return nil
		}
	}
}

// WatchIntercepts notifies a client or agent of the set of intercepts
// relevant to that client or agent.
func (m *Manager) WatchIntercepts(session *rpc.SessionInfo, stream rpc.Manager_WatchInterceptsServer) error {
	ctx := stream.Context()
	sessionID := session.SessionId

	dlog.Debugf(ctx, "WatchIntercepts called (sessionID=%q)", sessionID)

	entry := m.state.Get(sessionID)
	if entry == nil {
		return status.Errorf(codes.NotFound, "Session %q not found", sessionID)
	}

	sessionCtx := entry.Context()
	changed := m.state.WatchIntercepts(sessionID)

	for {
		res := &rpc.InterceptInfoSnapshot{
			Intercepts: m.state.GetIntercepts(sessionID),
		}
		if err := stream.Send(res); err != nil {
			return err
		}

		select {
		case <-changed:
			// It's time to send another message. Loop.
		case <-ctx.Done():
			// Manager is shutting down.
			return nil
		case <-sessionCtx.Done():
			// Manager believes this session has ended.
			return nil
		}
	}
}

// CreateIntercept lets a client create an intercept.
func (m *Manager) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	sessionID := ciReq.Session.SessionId
	spec := ciReq.InterceptSpec

	dlog.Debugf(ctx, "CreateIntercept called (sessionID=%q)", sessionID)

	if !m.state.HasClient(sessionID) {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	if val := validateIntercept(spec); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	for _, cept := range m.state.GetIntercepts(sessionID) {
		if cept.Spec.Name == spec.Name {
			return nil, status.Errorf(codes.AlreadyExists, "Intercept named %q already exists", spec.Name)
		}
	}

	return m.state.AddIntercept(sessionID, spec), nil
}

// RemoveIntercept lets a client remove an intercept.
func (m *Manager) RemoveIntercept(ctx context.Context, riReq *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	sessionID := riReq.Session.SessionId
	name := riReq.Name

	dlog.Debugf(ctx, "RemoveIntercept called (sessionID=%q, name=%q)", sessionID, name)

	if !m.state.HasClient(sessionID) {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	if !m.state.RemoveIntercept(sessionID, name) {
		return nil, status.Errorf(codes.NotFound, "Intercept named %q not found", name)
	}

	return &empty.Empty{}, nil
}

// ReviewIntercept lets an agent approve or reject an intercept.
func (m *Manager) ReviewIntercept(ctx context.Context, rIReq *rpc.ReviewInterceptRequest) (*empty.Empty, error) {
	sessionID := rIReq.Session.SessionId
	ceptID := rIReq.Id

	dlog.Debugf(ctx, "RemoveIntercept called (sessionID=%q, interceptID=%q)", sessionID, ceptID)

	if !m.state.HasAgent(sessionID) {
		return nil, status.Errorf(codes.NotFound, "Agent session %q not found", sessionID)
	}

	if !m.state.ReviewIntercept(sessionID, ceptID, rIReq.Disposition, rIReq.Message) {
		return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", ceptID)
	}

	return &empty.Empty{}, nil
}

// Expire removes stale sessions.
func (m *Manager) Expire() {
	m.state.Expire(15 * time.Second)
}

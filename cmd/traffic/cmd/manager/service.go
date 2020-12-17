package manager

import (
	"context"
	"sort"
	"time"

	"github.com/golang/protobuf/ptypes/empty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/telepresence2/cmd/traffic/cmd/manager/internal/state"
	rpc "github.com/datawire/telepresence2/pkg/rpc/manager"
	"github.com/datawire/telepresence2/pkg/rpc/systema"
	"github.com/datawire/telepresence2/pkg/version"
)

// Clock is the mechanism used by the Manager state to get the current time.
type Clock interface {
	Now() time.Time
}

type Manager struct {
	ctx     context.Context
	clock   Clock
	env     Env
	state   *state.State
	systema *systemaPool

	rpc.UnimplementedManagerServer
}

type wall struct{}

func (wall) Now() time.Time {
	return time.Now()
}

func NewManager(ctx context.Context, env Env) *Manager {
	ret := &Manager{
		ctx:   ctx,
		clock: wall{},
		env:   env,
		state: state.NewState(ctx),
	}
	ret.systema = NewSystemAPool(ret)
	return ret
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

	sessionID := m.state.AddClient(client, m.clock.Now())

	return &rpc.SessionInfo{SessionId: sessionID}, nil
}

// ArriveAsAgent establishes a session between an agent and the Manager.
func (m *Manager) ArriveAsAgent(ctx context.Context, agent *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	dlog.Debug(ctx, "ArriveAsAgent called")

	if val := validateAgent(agent); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	sessionID := m.state.AddAgent(agent, m.clock.Now())

	return &rpc.SessionInfo{SessionId: sessionID}, nil
}

// Remain indicates that the session is still valid.
func (m *Manager) Remain(ctx context.Context, req *rpc.RemainRequest) (*empty.Empty, error) {
	sessionID := req.Session.SessionId
	dlog.Debugf(ctx, "Remain called: %s", sessionID)

	if ok := m.state.MarkSession(req, m.clock.Now()); !ok {
		return nil, status.Errorf(codes.NotFound, "Session %q not found", sessionID)
	}

	return &empty.Empty{}, nil
}

// Depart terminates a session.
func (m *Manager) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	dlog.Debugf(ctx, "Depart called: %s", session.SessionId)

	m.state.RemoveSession(session.SessionId)

	return &empty.Empty{}, nil
}

// WatchAgents notifies a client of the set of known Agents.
func (m *Manager) WatchAgents(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentsServer) error {
	ctx := stream.Context()
	sessionID := session.SessionId

	dlog.Debugf(ctx, "WatchAgents called: %s", sessionID)

	snapshotCh := m.state.WatchAgents(ctx, nil)
	for {
		select {
		case snapshot := <-snapshotCh:
			agents := make([]*rpc.AgentInfo, 0, len(snapshot))
			for _, agent := range snapshot {
				agents = append(agents, agent)
			}
			resp := &rpc.AgentInfoSnapshot{
				Agents: agents,
			}
			if err := stream.Send(resp); err != nil {
				return err
			}
		case <-ctx.Done():
			// The request has been canceled.
			return nil
		case <-m.state.SessionDone(sessionID):
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

	dlog.Debugf(ctx, "WatchIntercepts called: %s", sessionID)

	var filter func(id string, info *rpc.InterceptInfo) bool
	if agent := m.state.GetAgent(sessionID); agent != nil {
		// sessionID refers to an agent session
		filter = func(id string, info *rpc.InterceptInfo) bool {
			return info.Spec.Agent == agent.Name
		}
	} else {
		// sessionID refers to a client session
		filter = func(id string, info *rpc.InterceptInfo) bool {
			return info.ClientSession.SessionId == sessionID
		}
	}

	snapshotCh := m.state.WatchIntercepts(ctx, filter)
	for {
		select {
		case snapshot := <-snapshotCh:
			dlog.Debugf(ctx, "WatchIntercepts sending update: %s", sessionID)
			intercepts := make([]*rpc.InterceptInfo, 0, len(snapshot))
			for _, intercept := range snapshot {
				intercepts = append(intercepts, intercept)
			}
			resp := &rpc.InterceptInfoSnapshot{
				Intercepts: intercepts,
			}
			sort.Slice(intercepts, func(i, j int) bool {
				return intercepts[i].Id < intercepts[j].Id
			})
			if err := stream.Send(resp); err != nil {
				dlog.Debugf(ctx, "WatchIntercepts encountered a write error: %v", err)
				return err
			}
		case <-ctx.Done():
			dlog.Debugf(ctx, "WatchIntercepts request cancelled: %s", sessionID)
			return nil
		case <-m.state.SessionDone(sessionID):
			dlog.Debugf(ctx, "WatchIntercepts session cancelled: %s", sessionID)
			return nil
		}
	}
}

// CreateIntercept lets a client create an intercept.
func (m *Manager) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	sessionID := ciReq.Session.SessionId
	spec := ciReq.InterceptSpec

	dlog.Debugf(ctx, "CreateIntercept called: %s", sessionID)

	if m.state.GetClient(sessionID) == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	if val := validateIntercept(spec); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	intercept, err := m.state.AddIntercept(sessionID, spec)
	if err != nil {
		return nil, err
	}

	if sa, err := m.systema.Get(); err != nil {
		dlog.Errorln(ctx, "systema:", err)
	} else {
		defer func() {
			if err := m.systema.Done(); err != nil {
				dlog.Errorln(ctx, "systema:", err)
			}
		}()
		resp, err := sa.CreateDomain(ctx, &systema.CreateDomainRequest{
			InterceptId: intercept.Id,
		})
		if err != nil {
			dlog.Errorln(ctx, "systema:", err)
		} else {
			intercept.PreviewDomain = resp.Domain
			m.state.UpdateIntercept(intercept)
		}
	}

	return intercept, nil
}

// RemoveIntercept lets a client remove an intercept.
func (m *Manager) RemoveIntercept(ctx context.Context, riReq *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	sessionID := riReq.Session.SessionId
	name := riReq.Name

	dlog.Debugf(ctx, "RemoveIntercept called: %s %s", sessionID, name)

	if m.state.GetClient(sessionID) == nil {
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

	dlog.Debugf(ctx, "ReviewIntercept called: %s %s - %s", sessionID, ceptID, rIReq.Disposition)

	if m.state.GetAgent(sessionID) == nil {
		return nil, status.Errorf(codes.NotFound, "Agent session %q not found", sessionID)
	}

	if !m.state.ReviewIntercept(sessionID, ceptID, rIReq.Disposition, rIReq.Message) {
		return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", ceptID)
	}

	return &empty.Empty{}, nil
}

// expire removes stale sessions.
func (m *Manager) expire() {
	m.state.ExpireSessions(m.clock.Now().Add(-15 * time.Second))
}

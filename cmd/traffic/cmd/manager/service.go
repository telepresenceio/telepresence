package manager

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

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
	ID      string
	state   *state.State
	systema *systemaPool

	rpc.UnsafeManagerServer
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
		ID:    uuid.New().String(),
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

	return &rpc.SessionInfo{
		SessionId:       sessionID,
		LicensedCluster: true,
	}, nil
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
		case snapshot, ok := <-snapshotCh:
			if !ok {
				// The request has been canceled.
				return nil
			}
			agents := make([]*rpc.AgentInfo, 0, len(snapshot.State))
			for _, agent := range snapshot.State {
				agents = append(agents, agent)
			}
			resp := &rpc.AgentInfoSnapshot{
				Agents: agents,
			}
			if err := stream.Send(resp); err != nil {
				return err
			}
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
			// Don't return intercepts for different agents.
			if info.Spec.Agent != agent.Name {
				return false
			}
			// Don't return intercepts that aren't in a "agent-owned" state.
			switch info.Disposition {
			case rpc.InterceptDispositionType_WAITING:
			case rpc.InterceptDispositionType_ACTIVE:
			case rpc.InterceptDispositionType_AGENT_ERROR:
				// agent-owned state: continue along
			default:
				// otherwise: don't return this intercept
				return false
			}
			// We haven't found a reason to exlude this intercept, so include it.
			return true
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
		case snapshot, ok := <-snapshotCh:
			if !ok {
				dlog.Debugf(ctx, "WatchIntercepts request cancelled: %s", sessionID)
				return nil
			}
			dlog.Debugf(ctx, "WatchIntercepts sending update: %s", sessionID)
			intercepts := make([]*rpc.InterceptInfo, 0, len(snapshot.State))
			for _, intercept := range snapshot.State {
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
		dlog.Errorln(ctx, "systema: acquire connection:", err)
	} else {
		resp, err := sa.CreateDomain(ctx, &systema.CreateDomainRequest{
			InterceptId:   intercept.Id,
			DisplayBanner: true, // FIXME(lukeshu): Don't hard-code this.
		})
		if err != nil {
			dlog.Errorln(ctx, "systema: create domain:", err)
			if err := m.systema.Done(); err != nil {
				dlog.Errorln(ctx, "systema: release connection:", err)
			}
		} else {
			_intercept := m.state.UpdateIntercept(intercept.Id, func(intercept *rpc.InterceptInfo) {
				intercept.PreviewDomain = resp.Domain
			})
			if _intercept == nil {
				// Someone else deleted the intercept while we were at it?
				_, err := sa.RemoveDomain(ctx, &systema.RemoveDomainRequest{
					Domain: resp.Domain,
				})
				if err != nil {
					dlog.Errorln(ctx, "systema: remove domain:", err)
				}
				if err := m.systema.Done(); err != nil {
					dlog.Errorln(ctx, "systema: release connection:", err)
				}
			} else {
				// Success!
				//
				// DON'T m.systema.Done(); keep the connection refcounted until the
				// intercept is deleted.
				intercept = _intercept
			}
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

	agent := m.state.GetAgent(sessionID)
	if agent == nil {
		return nil, status.Errorf(codes.NotFound, "Agent session %q not found", sessionID)
	}

	intercept := m.state.UpdateIntercept(ceptID, func(intercept *rpc.InterceptInfo) {
		// Sanity check: The reviewing agent must be an agent for the intercept.
		if intercept.Spec.Agent != agent.Name {
			return
		}

		// Only update intercepts in the waiting state.  Agents race to review an intercept, but we
		// expect they will always compatible answers.
		if intercept.Disposition == rpc.InterceptDispositionType_WAITING {
			intercept.Disposition = rIReq.Disposition
			intercept.Message = rIReq.Message
		}
	})

	if intercept == nil {
		return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", ceptID)
	}

	return &empty.Empty{}, nil
}

// expire removes stale sessions.
func (m *Manager) expire() {
	m.state.ExpireSessions(m.clock.Now().Add(-15 * time.Second))
}

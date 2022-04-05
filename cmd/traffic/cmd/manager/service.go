package manager

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/cluster"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/state"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Clock is the mechanism used by the Manager state to get the current time.
type Clock interface {
	Now() time.Time
}

type Manager struct {
	ctx         context.Context
	clock       Clock
	ID          string
	state       *state.State
	systema     *systemaPool
	clusterInfo cluster.Info

	rpc.UnsafeManagerServer
}

var _ rpc.ManagerServer = &Manager{}

type wall struct{}

func (wall) Now() time.Time {
	return time.Now()
}

func NewManager(ctx context.Context) *Manager {
	ret := &Manager{
		ctx:         ctx,
		clock:       wall{},
		ID:          uuid.New().String(),
		state:       state.NewState(ctx),
		clusterInfo: cluster.NewInfo(ctx),
	}
	ret.systema = NewSystemAPool(ret)
	return ret
}

// Version returns the version information of the Manager.
func (*Manager) Version(context.Context, *empty.Empty) (*rpc.VersionInfo2, error) {
	return &rpc.VersionInfo2{Version: version.Version}, nil
}

// GetLicense returns the license for the cluster. This directory is mounted
// via the connector if it detects the presence of a systema license secret
// when installing the traffic-manager
func (m *Manager) GetLicense(ctx context.Context, _ *empty.Empty) (*rpc.License, error) {
	clusterID := m.clusterInfo.GetClusterID()
	resp := &rpc.License{ClusterId: clusterID}
	// We want to be able to test GetClusterID easily in our integration tests,
	// so we return the error in the license.ErrMsg response RPC.
	var errMsg string

	// This is the actual license used by the cluster
	licenseData, err := os.ReadFile("/home/telepresence/license")
	if err != nil {
		errMsg += fmt.Sprintf("error reading license: %s\n", err)
	} else {
		resp.License = string(licenseData)
	}

	// This is the host domain associated with a license
	hostDomainData, err := os.ReadFile("/home/telepresence/hostDomain")
	if err != nil {
		errMsg += fmt.Sprintf("error reading hostDomain: %s\n", err)
	} else {
		resp.Host = string(hostDomainData)
	}

	if errMsg != "" {
		resp.ErrMsg = errMsg
	}
	return resp, nil
}

// CanConnectAmbassadorCloud checks if Ambassador Cloud is resolvable
// from within a cluster
func (m *Manager) CanConnectAmbassadorCloud(ctx context.Context, _ *empty.Empty) (*rpc.AmbassadorCloudConnection, error) {
	env := managerutil.GetEnv(ctx)
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%s", env.SystemAHost, env.SystemAPort), timeout)
	if err != nil {
		dlog.Debugf(ctx, "Failed to connect so assuming in air-gapped environment %s", err)
		return &rpc.AmbassadorCloudConnection{CanConnect: false}, nil
	}
	conn.Close()
	return &rpc.AmbassadorCloudConnection{CanConnect: true}, nil
}

// GetCloudConfig returns the SystemA Host and Port to the caller (currently just used by
// the agents)
func (m *Manager) GetCloudConfig(ctx context.Context, _ *empty.Empty) (*rpc.AmbassadorCloudConfig, error) {
	env := managerutil.GetEnv(ctx)
	return &rpc.AmbassadorCloudConfig{Host: env.SystemAHost, Port: env.SystemAPort}, nil
}

// GetTelepresenceAPI returns information about the TelepresenceAPI server
func (m *Manager) GetTelepresenceAPI(ctx context.Context, e *empty.Empty) (*rpc.TelepresenceAPIInfo, error) {
	env := managerutil.GetEnv(ctx)
	return &rpc.TelepresenceAPIInfo{Port: env.APIPort}, nil
}

// ArriveAsClient establishes a session between a client and the Manager.
func (m *Manager) ArriveAsClient(ctx context.Context, client *rpc.ClientInfo) (*rpc.SessionInfo, error) {
	dlog.Debug(ctx, "ArriveAsClient called")

	if val := validateClient(client); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	sessionID := m.state.AddClient(client, m.clock.Now())

	return &rpc.SessionInfo{
		SessionId: sessionID,
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
func (m *Manager) Remain(_ context.Context, req *rpc.RemainRequest) (*empty.Empty, error) {
	// ctx = WithSessionInfo(ctx, req.GetSession())
	// dlog.Debug(ctx, "Remain called")

	if ok := m.state.MarkSession(req, m.clock.Now()); !ok {
		return nil, status.Errorf(codes.NotFound, "Session %q not found", req.GetSession().GetSessionId())
	}

	return &empty.Empty{}, nil
}

// Depart terminates a session.
func (m *Manager) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, session)
	dlog.Debug(ctx, "Depart called")

	m.state.RemoveSession(ctx, session.GetSessionId())

	return &empty.Empty{}, nil
}

// WatchAgents notifies a client of the set of known Agents.
func (m *Manager) WatchAgents(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)

	dlog.Debug(ctx, "WatchAgents called")

	snapshotCh := m.state.WatchAgents(ctx, nil)
	sessionDone, err := m.state.SessionDone(session.GetSessionId())
	if err != nil {
		return err
	}
	for {
		select {
		case snapshot, ok := <-snapshotCh:
			if !ok {
				// The request has been canceled.
				dlog.Debug(ctx, "WatchAgents request cancelled")
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
		case <-sessionDone:
			// Manager believes this session has ended.
			dlog.Debug(ctx, "WatchAgents session cancelled")
			return nil
		}
	}
}

func infosEqual(a, b *rpc.AgentInfo) bool {
	ams := a.Mechanisms
	bms := b.Mechanisms
	if len(ams) != len(bms) {
		return false
	}
	ae := a.Environment
	be := b.Environment
	if len(ae) != len(be) {
		return false
	}
	if a.Name != b.Name || a.Namespace != b.Namespace || a.Product != b.Product || a.Version != b.Version {
		return false
	}
	for i, am := range ams {
		bm := bms[i]
		if am.Name != bm.Name || am.Product != bm.Product || am.Version != bm.Version {
			return false
		}
	}
	for k, av := range ae {
		if bv, ok := be[k]; !(ok && av == bv) {
			return false
		}
	}
	return true
}

// WatchAgentsNS notifies a client of the set of known Agents.
func (m *Manager) WatchAgentsNS(request *rpc.AgentsRequest, stream rpc.Manager_WatchAgentsNSServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), request.Session)

	dlog.Debug(ctx, "WatchAgentsNS called")

	snapshotCh := m.state.WatchAgents(ctx, nil)
	sessionDone, err := m.state.SessionDone(request.Session.GetSessionId())
	if err != nil {
		return err
	}

	// Ensure that initial snapshot is not equal to lastSnap even if it is empty so
	// that an initial snapshot is sent even when it's empty.
	lastSnap := make(map[string]*rpc.AgentInfo)
	lastSnap[""] = nil
	snapEqual := func(snap []*rpc.AgentInfo) bool {
		if len(snap) != len(lastSnap) {
			return false
		}
		for _, a := range snap {
			if b, ok := lastSnap[a.PodIp]; !ok || !infosEqual(a, b) {
				return false
			}
		}
		return true
	}

	includeAgent := func(a *rpc.AgentInfo) bool {
		for _, ns := range request.Namespaces {
			if ns == a.Namespace {
				return true
			}
		}
		return false
	}

	for {
		select {
		case snapshot, ok := <-snapshotCh:
			if !ok {
				// The request has been canceled.
				dlog.Debug(ctx, "WatchAgentsNS request cancelled")
				return nil
			}
			var agents []*rpc.AgentInfo
			for _, agent := range snapshot.State {
				if includeAgent(agent) {
					agents = append(agents, agent)
				}
			}
			if snapEqual(agents) {
				continue
			}
			lastSnap = make(map[string]*rpc.AgentInfo, len(agents))
			for _, a := range agents {
				lastSnap[a.PodIp] = a
			}
			resp := &rpc.AgentInfoSnapshot{
				Agents: agents,
			}
			if err := stream.Send(resp); err != nil {
				return err
			}
		case <-sessionDone:
			// Manager believes this session has ended.
			dlog.Debug(ctx, "WatchAgents session cancelled")
			return nil
		}
	}
}

// WatchIntercepts notifies a client or agent of the set of intercepts
// relevant to that client or agent.
func (m *Manager) WatchIntercepts(session *rpc.SessionInfo, stream rpc.Manager_WatchInterceptsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	sessionID := session.GetSessionId()

	dlog.Debug(ctx, "WatchIntercepts called")

	var filter func(id string, info *rpc.InterceptInfo) bool
	if sessionID == "" {
		// No sessonID; watch everything
		filter = func(id string, info *rpc.InterceptInfo) bool {
			return true
		}
	} else if agent := m.state.GetAgent(sessionID); agent != nil {
		// sessionID refers to an agent session
		filter = func(id string, info *rpc.InterceptInfo) bool {
			// Don't return intercepts for different agents.
			if info.Spec.Namespace != agent.Namespace || info.Spec.Agent != agent.Name {
				dlog.Debugf(ctx, "Intercept mismatch: %s.%s != %s.%s", info.Spec.Agent, info.Spec.Namespace, agent.Name, agent.Namespace)
				return false
			}
			// Don't return intercepts that aren't in a "agent-owned" state.
			switch info.Disposition {
			case rpc.InterceptDispositionType_WAITING,
				rpc.InterceptDispositionType_ACTIVE,
				rpc.InterceptDispositionType_AGENT_ERROR:
				// agent-owned state: include the intercept
				dlog.Debugf(ctx, "Intercept %s.%s valid. Disposition: %s", info.Spec.Agent, info.Spec.Namespace, info.Disposition)
				return true
			default:
				// otherwise: don't return this intercept
				dlog.Debugf(ctx, "Intercept %s.%s is not in agent-owned state. Disposition: %s", info.Spec.Agent, info.Spec.Namespace, info.Disposition)
				return false
			}
		}
	} else {
		// sessionID refers to a client session
		filter = func(id string, info *rpc.InterceptInfo) bool {
			return info.ClientSession.SessionId == sessionID
		}
	}

	var sessionDone <-chan struct{}
	if sessionID == "" {
		ch := make(chan struct{})
		defer close(ch)
		sessionDone = ch
	} else {
		var err error
		if sessionDone, err = m.state.SessionDone(sessionID); err != nil {
			return err
		}
	}

	snapshotCh := m.state.WatchIntercepts(ctx, filter)
	for {
		select {
		case snapshot, ok := <-snapshotCh:
			if !ok {
				dlog.Debugf(ctx, "WatchIntercepts request cancelled")
				return nil
			}
			dlog.Debugf(ctx, "WatchIntercepts sending update")
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
		case <-sessionDone:
			dlog.Debugf(ctx, "WatchIntercepts session cancelled")
			return nil
		}
	}
}

// CreateIntercept lets a client create an intercept.
func (m *Manager) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	ctx = managerutil.WithSessionInfo(ctx, ciReq.GetSession())
	sessionID := ciReq.GetSession().GetSessionId()
	spec := ciReq.InterceptSpec
	apiKey := ciReq.GetApiKey()
	dlog.Debug(ctx, "CreateIntercept called")

	if m.state.GetClient(sessionID) == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	if val := validateIntercept(spec); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	return m.state.AddIntercept(sessionID, apiKey, spec)
}

func (m *Manager) makeinterceptID(ctx context.Context, sessionID string, name string) (string, error) {
	// When something without a session ID (e.g. System A) calls this function,
	// it is sending the intercept ID as the name, so we use that.
	//
	// TODO: Look at cmd/traffic/cmd/manager/internal/state API and see if it makes
	// sense to make more / all functions use intercept ID instead of session ID + name.
	// Or at least functions outside services (e.g. SystemA), which don't know about sessions,
	// use in requests.
	if sessionID == "" {
		return name, nil
	} else {
		if m.state.GetClient(sessionID) == nil {
			return "", status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
		}
		return sessionID + ":" + name, nil
	}
}

const systemaCallTimeout = 3 * time.Second

func (m *Manager) UpdateIntercept(ctx context.Context, req *rpc.UpdateInterceptRequest) (*rpc.InterceptInfo, error) { //nolint:gocognit
	ctx = managerutil.WithSessionInfo(ctx, req.GetSession())
	interceptID, err := m.makeinterceptID(ctx, req.GetSession().GetSessionId(), req.GetName())
	if err != nil {
		return nil, err
	}

	dlog.Debugf(ctx, "UpdateIntercept called: %s", interceptID)

	switch action := req.PreviewDomainAction.(type) {
	case *rpc.UpdateInterceptRequest_AddPreviewDomain:
		var domain string
		var sa systema.SystemACRUDClient
		var err error
		intercept := m.state.UpdateIntercept(interceptID, func(intercept *rpc.InterceptInfo) {
			// Check if this is already done.
			if intercept.PreviewDomain != "" {
				return
			}

			// Connect to SystemA.
			if sa == nil {
				sa, err = m.systema.Get()
				if err != nil {
					err = errors.Wrap(err, "systema: acquire connection")
					return
				}
			}

			// Have SystemA create the preview domain.
			if domain == "" {
				tc, cancel := context.WithTimeout(ctx, systemaCallTimeout)
				defer cancel()
				var resp *systema.CreateDomainResponse
				resp, err = sa.CreateDomain(tc, &systema.CreateDomainRequest{
					InterceptId:   intercept.Id,
					DisplayBanner: action.AddPreviewDomain.DisplayBanner,
					InterceptSpec: intercept.Spec,
					Host:          action.AddPreviewDomain.Ingress.L5Host,
				})
				if err != nil {
					err = errors.Wrap(err, "systema: create domain")
					return
				}
				domain = resp.Domain
			}

			// Apply that to the intercept.
			intercept.PreviewDomain = domain
			intercept.PreviewSpec = action.AddPreviewDomain
		})
		if err != nil || intercept == nil || domain == "" || intercept.PreviewDomain != domain {
			// Oh no, something went wrong.  Clean up.
			if sa != nil {
				if domain != "" {
					tc, cancel := context.WithTimeout(ctx, systemaCallTimeout)
					defer cancel()
					_, err := sa.RemoveDomain(tc, &systema.RemoveDomainRequest{
						Domain: domain,
					})
					if err != nil {
						dlog.Errorln(ctx, "systema: remove domain:", err)
					}
				}
				if err := m.systema.Done(); err != nil {
					dlog.Errorln(ctx, "systema: release connection:", err)
				}
			}
		}
		if intercept == nil {
			err = status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", interceptID)
		}
		return intercept, err
	case *rpc.UpdateInterceptRequest_RemovePreviewDomain:
		var domain string
		intercept := m.state.UpdateIntercept(interceptID, func(intercept *rpc.InterceptInfo) {
			// Check if this is already done.
			if intercept.PreviewDomain == "" {
				return
			}

			// Remove the domain
			domain = intercept.PreviewDomain
			intercept.PreviewDomain = ""
		})
		if domain != "" {
			if sa, err := m.systema.Get(); err != nil {
				dlog.Errorln(ctx, "systema: acquire connection:", err)
			} else {
				tc, cancel := context.WithTimeout(ctx, systemaCallTimeout)
				defer cancel()
				_, err := sa.RemoveDomain(tc, &systema.RemoveDomainRequest{
					Domain: domain,
				})
				if err != nil {
					dlog.Errorln(ctx, "systema: remove domain:", err)
				}
				if err := m.systema.Done(); err != nil {
					dlog.Errorln(ctx, "systema: release connection:", err)
				}
			}
		}
		if intercept == nil {
			return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", interceptID)
		}
		return intercept, nil
	default:
		panic(errors.Errorf("Unimplemented UpdateInterceptRequest action: %T", action))
	}
}

// RemoveIntercept lets a client remove an intercept.
func (m *Manager) RemoveIntercept(ctx context.Context, riReq *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, riReq.GetSession())
	sessionID := riReq.GetSession().GetSessionId()
	name := riReq.Name

	dlog.Debugf(ctx, "RemoveIntercept called: %s", name)

	if m.state.GetClient(sessionID) == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	if !m.state.RemoveIntercept(sessionID + ":" + name) {
		return nil, status.Errorf(codes.NotFound, "Intercept named %q not found", name)
	}

	return &empty.Empty{}, nil
}

// GetIntercept gets an intercept info from intercept name
func (m *Manager) GetIntercept(ctx context.Context, request *rpc.GetInterceptRequest) (*rpc.InterceptInfo, error) {
	interceptID, err := m.makeinterceptID(ctx, request.GetSession().GetSessionId(), request.GetName())
	if err != nil {
		return nil, err
	}
	if intercept, ok := m.state.GetIntercept(interceptID); ok {
		return intercept, nil
	} else {
		return nil, status.Errorf(codes.NotFound, "Intercept named %q not found", request.Name)
	}
}

// ReviewIntercept lets an agent approve or reject an intercept.
func (m *Manager) ReviewIntercept(ctx context.Context, rIReq *rpc.ReviewInterceptRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, rIReq.GetSession())
	sessionID := rIReq.GetSession().GetSessionId()
	ceptID := rIReq.Id

	dlog.Debugf(ctx, "ReviewIntercept called: %s - %s", ceptID, rIReq.Disposition)

	agent := m.state.GetAgent(sessionID)
	if agent == nil {
		return nil, status.Errorf(codes.NotFound, "Agent session %q not found", sessionID)
	}

	intercept := m.state.UpdateIntercept(ceptID, func(intercept *rpc.InterceptInfo) {
		// Sanity check: The reviewing agent must be an agent for the intercept.
		if intercept.Spec.Namespace != agent.Namespace || intercept.Spec.Agent != agent.Name {
			return
		}

		// Only update intercepts in the waiting state.  Agents race to review an intercept, but we
		// expect they will always compatible answers.
		if intercept.Disposition == rpc.InterceptDispositionType_WAITING {
			intercept.Disposition = rIReq.Disposition
			intercept.Message = rIReq.Message
			intercept.PodIp = rIReq.PodIp
			intercept.SftpPort = rIReq.SftpPort
			intercept.MechanismArgsDesc = rIReq.MechanismArgsDesc
			intercept.Headers = rIReq.Headers
			intercept.Metadata = rIReq.Metadata
		}
	})

	if intercept == nil {
		return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", ceptID)
	}

	return &empty.Empty{}, nil
}

func (m *Manager) Tunnel(server rpc.Manager_TunnelServer) error {
	ctx := server.Context()
	stream, err := tunnel.NewServerStream(ctx, server)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to connect stream: %v", err)
	}
	return m.state.Tunnel(ctx, stream)
}

func (m *Manager) WatchDial(session *rpc.SessionInfo, stream rpc.Manager_WatchDialServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchDial called")
	lrCh := m.state.WatchDial(session.SessionId)
	for {
		select {
		case <-m.ctx.Done():
			return nil
		case lr := <-lrCh:
			if lr == nil {
				return nil
			}
			if err := stream.Send(lr); err != nil {
				dlog.Errorf(ctx, "WatchDial.Send() failed: %v", err)
				return nil
			}
		}
	}
}

func (m *Manager) LookupHost(ctx context.Context, request *rpc.LookupHostRequest) (*rpc.LookupHostResponse, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSession())
	dlog.Debugf(ctx, "LookupHost called %s", request.Host)
	sessionID := request.GetSession().GetSessionId()

	ips, count, err := m.state.AgentsLookup(ctx, sessionID, request)
	if err != nil {
		dlog.Errorf(ctx, "AgentLookup: %v", err)
	} else if count > 0 {
		if len(ips) == 0 {
			dlog.Debugf(ctx, "LookupHost on agents: %s -> NOT FOUND", request.Host)
		} else {
			dlog.Debugf(ctx, "LookupHost on agents: %s -> %s", request.Host, ips)
		}
	}

	if count == 0 {
		if addrs, err := net.DefaultResolver.LookupHost(ctx, request.Host); err != nil {
			if dnsErr, ok := err.(*net.DNSError); ok && dnsErr.IsNotFound {
				dlog.Debugf(ctx, "LookupHost on traffic-manager: %s -> NOT FOUND", request.Host)
			} else {
				dlog.Errorf(ctx, "LookupHost on traffic-manager LookupHost: %v", err)
			}
		} else {
			ips = make(iputil.IPs, len(addrs))
			for i, addr := range addrs {
				ips[i] = iputil.Parse(addr)
			}
			dlog.Debugf(ctx, "LookupHost on traffic-manager: %s -> %s", request.Host, ips)
		}
	}
	if ips == nil {
		ips = iputil.IPs{}
	}
	return &rpc.LookupHostResponse{Ips: ips.BytesSlice()}, nil
}

func (m *Manager) AgentLookupHostResponse(ctx context.Context, response *rpc.LookupHostAgentResponse) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, response.GetSession())
	dlog.Debugf(ctx, "AgentLookupHostResponse called %s -> %s", response.Request.Host, iputil.IPsFromBytesSlice(response.Response.Ips))
	m.state.PostLookupResponse(response)
	return &empty.Empty{}, nil
}

func (m *Manager) WatchLookupHost(session *rpc.SessionInfo, stream rpc.Manager_WatchLookupHostServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchLookupHost called")
	lrCh := m.state.WatchLookupHost(session.SessionId)
	for {
		select {
		case <-m.ctx.Done():
			return nil
		case lr := <-lrCh:
			if lr == nil {
				return nil
			}
			if err := stream.Send(lr); err != nil {
				dlog.Errorf(ctx, "WatchLookupHost.Send() failed: %v", err)
				return nil
			}
		}
	}
}

// GetLogs acquires the logs for the traffic-manager and/or traffic-agents specified by the
// GetLogsRequest and returns them to the caller
// Deprecated: Clients should use the user daemon's GatherLogs method
func (m *Manager) GetLogs(_ context.Context, _ *rpc.GetLogsRequest) (*rpc.LogsResponse, error) {
	return &rpc.LogsResponse{
		PodLogs: make(map[string]string),
		PodYaml: make(map[string]string),
		ErrMsg:  "traffic-manager.GetLogs is deprecated. Please upgrade your telepresence client",
	}, nil
}

func (m *Manager) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (*empty.Empty, error) {
	m.state.SetTempLogLevel(ctx, request)
	return &empty.Empty{}, nil
}

func (m *Manager) WatchLogLevel(_ *empty.Empty, stream rpc.Manager_WatchLogLevelServer) error {
	dlog.Debugf(stream.Context(), "WatchLogLevel called")
	return m.state.WaitForTempLogLevel(stream)
}

func (m *Manager) WatchClusterInfo(session *rpc.SessionInfo, stream rpc.Manager_WatchClusterInfoServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchClusterInfo called")
	return m.clusterInfo.Watch(ctx, stream)
}

const clientSessionTTL = 24 * time.Hour
const agentSessionTTL = 15 * time.Second

// expire removes stale sessions.
func (m *Manager) expire(ctx context.Context) {
	now := m.clock.Now()
	m.state.ExpireSessions(ctx, now.Add(-clientSessionTTL), now.Add(-agentSessionTTL))
}

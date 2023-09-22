package manager

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	dns2 "github.com/miekg/dns"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/cluster"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// Clock is the mechanism used by the Manager state to get the current time.
type Clock interface {
	Now() time.Time
}

type Service interface {
	rpc.ManagerServer
	ID() string
	InstallID() string
	MakeInterceptID(context.Context, string, string) (string, error)
	RegisterServers(*grpc.Server)
	TrafficManagerConfig() []byte
	State() state.State

	// unexported methods.
	runConfigWatcher(context.Context) error
	runSessionGCLoop(context.Context) error
	serveHTTP(context.Context) error
	servePrometheus(context.Context) error
}

type service struct {
	ctx                context.Context
	clock              Clock
	id                 string
	state              state.State
	clusterInfo        cluster.Info
	configWatcher      config.Watcher
	activeHttpRequests int32
	activeGrpcRequests int32

	// Possibly extended version of the service. Use when calling interface methods.
	self Service

	rpc.UnsafeManagerServer
}

var _ rpc.ManagerServer = &service{}

type wall struct{}

func (wall) Now() time.Time {
	return time.Now()
}

func NewService(ctx context.Context) (Service, context.Context, error) {
	ret := &service{
		clock: wall{},
		id:    uuid.New().String(),
	}
	ret.configWatcher = config.NewWatcher(managerutil.GetEnv(ctx).ManagerNamespace)
	ret.ctx = ctx
	// These are context dependent so build them once the pool is up
	ret.clusterInfo = cluster.NewInfo(ctx)
	ret.state = state.NewStateFunc(ctx)
	ret.self = ret
	return ret, ctx, nil
}

func (s *service) SetSelf(self Service) {
	s.self = self
}

func (s *service) ClusterInfo() cluster.Info {
	return s.clusterInfo
}

func (s *service) ID() string {
	return s.id
}

func (s *service) State() state.State {
	return s.state
}

func (s *service) InstallID() string {
	return s.clusterInfo.ID()
}

func (s *service) TrafficManagerConfig() []byte {
	return s.configWatcher.GetTrafficManagerConfigYaml()
}

func (s *service) runConfigWatcher(ctx context.Context) error {
	return s.configWatcher.Run(ctx)
}

// Version returns the version information of the Manager.
func (*service) Version(context.Context, *empty.Empty) (*rpc.VersionInfo2, error) {
	return &rpc.VersionInfo2{Name: DisplayName, Version: version.Version}, nil
}

func (s *service) GetLicense(context.Context, *empty.Empty) (*rpc.License, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *service) CanConnectAmbassadorCloud(context.Context, *empty.Empty) (*rpc.AmbassadorCloudConnection, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

func (s *service) GetCloudConfig(context.Context, *empty.Empty) (*rpc.AmbassadorCloudConfig, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// GetTelepresenceAPI returns information about the TelepresenceAPI server.
func (s *service) GetTelepresenceAPI(ctx context.Context, e *empty.Empty) (*rpc.TelepresenceAPIInfo, error) {
	env := managerutil.GetEnv(ctx)
	return &rpc.TelepresenceAPIInfo{Port: int32(env.APIPort)}, nil
}

// ArriveAsClient establishes a session between a client and the Manager.
func (s *service) ArriveAsClient(ctx context.Context, client *rpc.ClientInfo) (*rpc.SessionInfo, error) {
	dlog.Debug(ctx, "ArriveAsClient called")

	if val := validateClient(client); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	installId := client.GetInstallId()
	return &rpc.SessionInfo{
		SessionId: s.state.AddClient(client, s.clock.Now()),
		ClusterId: s.clusterInfo.ID(),
		InstallId: &installId,
	}, nil
}

// ArriveAsAgent establishes a session between an agent and the Manager.
func (s *service) ArriveAsAgent(ctx context.Context, agent *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	dlog.Debug(ctx, "ArriveAsAgent called")

	if val := validateAgent(agent); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	sessionID := s.state.AddAgent(agent, s.clock.Now())

	return &rpc.SessionInfo{
		SessionId: sessionID,
		ClusterId: s.clusterInfo.ID(),
	}, nil
}

func (s *service) GetClientConfig(ctx context.Context, _ *empty.Empty) (*rpc.CLIConfig, error) {
	dlog.Debug(ctx, "GetClientConfig called")

	return &rpc.CLIConfig{
		ConfigYaml: s.configWatcher.GetClientConfigYaml(),
	}, nil
}

// Remain indicates that the session is still valid.
func (s *service) Remain(ctx context.Context, req *rpc.RemainRequest) (*empty.Empty, error) {
	// ctx = WithSessionInfo(ctx, req.GetSession())
	// dlog.Debug(ctx, "Remain called")
	sessionID := req.GetSession().GetSessionId()
	if ok := s.state.MarkSession(req, s.clock.Now()); !ok {
		return nil, status.Errorf(codes.NotFound, "Session %q not found", sessionID)
	}

	s.state.RefreshSessionConsumptionMetrics(sessionID)

	return &empty.Empty{}, nil
}

// Depart terminates a session.
func (s *service) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, session)
	dlog.Debug(ctx, "Depart called")

	if err := s.state.RemoveSession(ctx, session.GetSessionId()); err != nil {
		return nil, err
	}

	return &empty.Empty{}, nil
}

// WatchAgents notifies a client of the set of known Agents.
func (s *service) WatchAgents(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)

	dlog.Debug(ctx, "WatchAgents called")

	snapshotCh := s.state.WatchAgents(ctx, nil)
	sessionDone, err := s.state.SessionDone(session.GetSessionId())
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
			as := snapshot.State
			agents := make([]*rpc.AgentInfo, len(as))
			names := make([]string, len(as))
			i := 0
			for _, a := range as {
				agents[i] = a
				names[i] = a.Name + "." + a.Namespace
				i++
			}
			resp := &rpc.AgentInfoSnapshot{
				Agents: agents,
			}
			dlog.Debugf(ctx, "WatchAgents sending update %v", names)
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
func (s *service) WatchAgentsNS(request *rpc.AgentsRequest, stream rpc.Manager_WatchAgentsNSServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), request.Session)

	dlog.Debug(ctx, "WatchAgentsNS called")

	snapshotCh := s.state.WatchAgents(ctx, nil)
	sessionDone, err := s.state.SessionDone(request.Session.GetSessionId())
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
			names := make([]string, len(agents))
			i := 0
			for _, a := range agents {
				names[i] = a.Name + "." + a.Namespace
				i++
			}
			dlog.Debugf(ctx, "WatchAgentsNS sending update %v", names)
			resp := &rpc.AgentInfoSnapshot{
				Agents: agents,
			}
			if err := stream.Send(resp); err != nil {
				return err
			}
		case <-sessionDone:
			// Manager believes this session has ended.
			dlog.Debug(ctx, "WatchAgentsNS session cancelled")
			return nil
		}
	}
}

// WatchIntercepts notifies a client or agent of the set of intercepts
// relevant to that client or agent.
func (s *service) WatchIntercepts(session *rpc.SessionInfo, stream rpc.Manager_WatchInterceptsServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	sessionID := session.GetSessionId()

	dlog.Debug(ctx, "WatchIntercepts called")

	var sessionDone <-chan struct{}
	var filter func(id string, info *rpc.InterceptInfo) bool
	if sessionID == "" {
		// No sessonID; watch everything
		filter = func(id string, info *rpc.InterceptInfo) bool {
			return true
		}
	} else {
		var err error
		if sessionDone, err = s.state.SessionDone(sessionID); err != nil {
			return err
		}

		if agent := s.state.GetAgent(sessionID); agent != nil {
			// sessionID refers to an agent session
			filter = func(id string, info *rpc.InterceptInfo) bool {
				if info.Spec.Namespace != agent.Namespace || info.Spec.Agent != agent.Name {
					// Don't return intercepts for different agents.
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
	}

	snapshotCh := s.state.WatchIntercepts(ctx, filter)
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
		case <-ctx.Done():
			dlog.Debugf(ctx, "WatchIntercepts context cancelled")
			return nil
		case <-sessionDone:
			dlog.Debugf(ctx, "WatchIntercepts session cancelled")
			return nil
		}
	}
}

func (s *service) PrepareIntercept(ctx context.Context, request *rpc.CreateInterceptRequest) (*rpc.PreparedIntercept, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.Session)
	dlog.Debugf(ctx, "PrepareIntercept called")

	span := trace.SpanFromContext(ctx)
	tracing.RecordInterceptSpec(span, request.InterceptSpec)

	replacePolicy := agentconfig.ReplacePolicyNever
	if request.InterceptSpec.Replace {
		replacePolicy = agentconfig.ReplacePolicyInactive
	}
	return s.state.PrepareIntercept(ctx, request, replacePolicy)
}

// CreateIntercept lets a client create an intercept.
func (s *service) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	ctx = managerutil.WithSessionInfo(ctx, ciReq.GetSession())
	sessionID := ciReq.GetSession().GetSessionId()
	spec := ciReq.InterceptSpec
	dlog.Debug(ctx, "CreateIntercept called")
	span := trace.SpanFromContext(ctx)
	tracing.RecordInterceptSpec(span, spec)

	client := s.state.GetClient(sessionID)

	if client == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	if val := validateIntercept(spec); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	if ciReq.InterceptSpec.Replace {
		_, err := s.state.PrepareIntercept(ctx, ciReq, agentconfig.ReplacePolicyActive)
		if err != nil {
			return nil, err
		}
	}

	interceptInfo, err := s.state.AddIntercept(ctx, sessionID, s.clusterInfo.ID(), client, ciReq)
	if err != nil {
		return nil, err
	}
	if interceptInfo != nil {
		tracing.RecordInterceptInfo(span, interceptInfo)
	}

	if ciReq.InterceptSpec.Replace {
		err := s.state.AddInterceptFinalizer(interceptInfo.Id, func(ctx context.Context, info *rpc.InterceptInfo) error {
			dlog.Debugf(ctx, "Restoring app container for %s", info.Id)
			ciReq.InterceptSpec.Replace = false
			_, err := s.state.PrepareIntercept(ctx, ciReq, agentconfig.ReplacePolicyInactive)
			return err
		})
		if err != nil {
			// The intercept's been created but we can't finalize it...
			dlog.Errorf(ctx, "Failed to add finalizer for %s: %v", interceptInfo.Id, err)
		}
	}

	return interceptInfo, nil
}

func (s *service) MakeInterceptID(_ context.Context, sessionID string, name string) (string, error) {
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
		if s.state.GetClient(sessionID) == nil {
			return "", status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
		}
		return sessionID + ":" + name, nil
	}
}

func (s *service) UpdateIntercept(context.Context, *rpc.UpdateInterceptRequest) (*rpc.InterceptInfo, error) { //nolint:gocognit
	return nil, status.Error(codes.Unimplemented, "")
}

// RemoveIntercept lets a client remove an intercept.
func (s *service) RemoveIntercept(ctx context.Context, riReq *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, riReq.GetSession())
	sessionID := riReq.GetSession().GetSessionId()
	name := riReq.Name

	dlog.Debugf(ctx, "RemoveIntercept called: %s", name)

	if s.state.GetClient(sessionID) == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	if removed, err := s.state.RemoveIntercept(ctx, sessionID+":"+name); err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to finalize intercept %q: %v", name, err)
	} else if !removed {
		return nil, status.Errorf(codes.NotFound, "Intercept named %q not found", name)
	}

	return &empty.Empty{}, nil
}

// GetIntercept gets an intercept info from intercept name.
func (s *service) GetIntercept(ctx context.Context, request *rpc.GetInterceptRequest) (*rpc.InterceptInfo, error) {
	interceptID, err := s.MakeInterceptID(ctx, request.GetSession().GetSessionId(), request.GetName())
	if err != nil {
		return nil, err
	}
	if intercept, ok := s.state.GetIntercept(interceptID); ok {
		return intercept, nil
	} else {
		return nil, status.Errorf(codes.NotFound, "Intercept named %q not found", request.Name)
	}
}

// ReviewIntercept lets an agent approve or reject an intercept.
func (s *service) ReviewIntercept(ctx context.Context, rIReq *rpc.ReviewInterceptRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, rIReq.GetSession())
	sessionID := rIReq.GetSession().GetSessionId()
	ceptID := rIReq.Id

	dlog.Debugf(ctx, "ReviewIntercept called: %s - %s", ceptID, rIReq.Disposition)

	agent := s.state.GetAgent(sessionID)
	if agent == nil {
		return nil, status.Errorf(codes.NotFound, "Agent session %q not found", sessionID)
	}

	rIReq.Environment = s.removeExcludedEnvVars(ctx, rIReq.Environment)

	intercept := s.state.UpdateIntercept(ceptID, func(intercept *rpc.InterceptInfo) {
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
			intercept.FtpPort = rIReq.FtpPort
			intercept.SftpPort = rIReq.SftpPort
			intercept.MountPoint = rIReq.MountPoint
			intercept.MechanismArgsDesc = rIReq.MechanismArgsDesc
			intercept.Headers = rIReq.Headers
			intercept.Metadata = rIReq.Metadata
			intercept.Environment = rIReq.Environment
		}
	})

	if intercept == nil {
		return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", ceptID)
	}

	return &empty.Empty{}, nil
}

func (s *service) removeExcludedEnvVars(ctx context.Context, envVars map[string]string) map[string]string {
	cm, err := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(managerutil.GetEnv(ctx).ManagerNamespace).Get(ctx, "telepresence-intercept-env", v1.GetOptions{})
	if err != nil {
		dlog.Errorf(ctx, "cannot read excluded variables configmap: %v", err)
		return envVars
	}

	keys := strings.Split(cm.Data["excluded"], "\n")
	for _, key := range keys {
		delete(envVars, key)
	}

	return envVars
}

func (s *service) Tunnel(server rpc.Manager_TunnelServer) error {
	ctx := server.Context()
	stream, err := tunnel.NewServerStream(ctx, server)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to connect stream: %v", err)
	}
	return s.state.Tunnel(ctx, stream)
}

func (s *service) WatchDial(session *rpc.SessionInfo, stream rpc.Manager_WatchDialServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchDial called")
	lrCh := s.state.WatchDial(session.SessionId)
	for {
		select {
		// connection broken
		case <-ctx.Done():
			return nil
		// service stopped
		case <-s.ctx.Done():
			return nil
		case lr := <-lrCh:
			if lr == nil {
				return nil
			}
			if err := stream.Send(lr); err != nil {
				dlog.Errorf(ctx, "failed to send dial request: %v", err)
				// We couldn't stream the dial request. This likely means
				// that we lost connection.
				return nil
			}
		}
	}
}

// LookupHost
// Deprecated: Use LookupDNS
//
//nolint:staticcheck // retained for backward compatibility
func (s *service) LookupHost(ctx context.Context, request *rpc.LookupHostRequest) (*rpc.LookupHostResponse, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSession())
	dlog.Debugf(ctx, "LookupHost %s", request.Name)

	// Use LookupDNS internally
	rsp, err := s.LookupDNS(ctx, &rpc.DNSRequest{
		Session: request.Session,
		Name:    request.Name + ".",
		Type:    uint32(dns2.TypeA),
	})
	if err != nil {
		return nil, err
	}
	rrs, rcode, err := dnsproxy.FromRPC(rsp)
	if err != nil {
		return nil, err
	}
	var ips iputil.IPs
	if rcode == dns2.RcodeSuccess {
		for _, rr := range rrs {
			if ar, ok := rr.(*dns2.A); ok {
				ips = append(ips, ar.A)
			}
		}
	}
	if ips == nil {
		ips = iputil.IPs{}
	}
	return &rpc.LookupHostResponse{Ips: ips.BytesSlice()}, nil
}

// AgentLookupHostResponse
// Deprecated: More recent clients will use LookupDNS
//
//nolint:staticcheck // retained for backward compatibility
func (s *service) AgentLookupHostResponse(ctx context.Context, response *rpc.LookupHostAgentResponse) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, response.GetSession())
	ips := iputil.IPsFromBytesSlice(response.Response.Ips)
	request := response.Request
	dlog.Debugf(ctx, "AgentLookupHostResponse called %s -> %s", request.Name, ips)
	rcode := dns2.RcodeNameError
	var rrs dnsproxy.RRs
	if len(ips) > 0 {
		rcode = dns2.RcodeSuccess
		rrs = make(dnsproxy.RRs, len(ips))
		for i, ip := range ips {
			rrs[i] = &dns2.A{Hdr: dnsproxy.NewHeader(request.Name, dns2.TypeA), A: ip}
		}
	}
	rsp, err := dnsproxy.ToRPC(rrs, rcode)
	if err != nil {
		return nil, err
	}
	s.state.PostLookupDNSResponse(ctx, &rpc.DNSAgentResponse{
		Session: response.Session,
		Request: &rpc.DNSRequest{
			Session: request.Session,
			Name:    request.Name,
			Type:    uint32(dns2.TypeA),
		},
		Response: rsp,
	})
	return &empty.Empty{}, nil
}

// WatchLookupHost
// Deprecated: retained for backward compatibility. More recent clients will use LookupDNS.
func (s *service) WatchLookupHost(session *rpc.SessionInfo, stream rpc.Manager_WatchLookupHostServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchLookupHost called")
	rqCh := s.state.WatchLookupDNS(session.SessionId)
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case rq := <-rqCh:
			if rq == nil {
				return nil
			}
			if err := stream.Send(&rpc.LookupHostRequest{Session: rq.Session, Name: rq.Name}); err != nil {
				dlog.Errorf(ctx, "WatchLookupHost.Send() failed: %v", err)
				return nil
			}
		}
	}
}

func (s *service) LookupDNS(ctx context.Context, request *rpc.DNSRequest) (*rpc.DNSResponse, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSession())
	qType := uint16(request.Type)
	qtn := dns2.TypeToString[qType]
	dlog.Debugf(ctx, "LookupDNS %s %s", request.Name, qtn)

	rrs, rCode, err := s.state.AgentsLookupDNS(ctx, request.GetSession().GetSessionId(), request)
	if err != nil {
		dlog.Errorf(ctx, "AgentsLookupDNS %s %s: %v", request.Name, qtn, err)
	} else if rCode != state.RcodeNoAgents {
		if len(rrs) == 0 {
			dlog.Debugf(ctx, "LookupDNS on agents: %s %s -> %s", request.Name, qtn, dns2.RcodeToString[rCode])
		} else {
			dlog.Debugf(ctx, "LookupDNS on agents: %s %s -> %s", request.Name, qtn, rrs)
		}
	}
	if rCode == state.RcodeNoAgents {
		rrs, rCode, err = dnsproxy.Lookup(ctx, qType, request.Name)
		if err != nil {
			dlog.Debugf(ctx, "LookupDNS on traffic-manager: %s %s -> %s %s", request.Name, qtn, dns2.RcodeToString[rCode], err)
			return nil, err
		}
		if len(rrs) == 0 {
			dlog.Debugf(ctx, "LookupDNS on traffic-manager: %s %s -> %s", request.Name, qtn, dns2.RcodeToString[rCode])
		} else {
			dlog.Debugf(ctx, "LookupDNS on traffic-manager: %s %s -> %s", request.Name, qtn, rrs)
		}
	}
	return dnsproxy.ToRPC(rrs, rCode)
}

func (s *service) AgentLookupDNSResponse(ctx context.Context, response *rpc.DNSAgentResponse) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, response.GetSession())
	dlog.Debugf(ctx, "AgentLookupDNSResponse called %s", response.Request.Name)
	s.state.PostLookupDNSResponse(ctx, response)
	return &empty.Empty{}, nil
}

func (s *service) WatchLookupDNS(session *rpc.SessionInfo, stream rpc.Manager_WatchLookupDNSServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchLookupDNS called")
	rqCh := s.state.WatchLookupDNS(session.SessionId)
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case rq := <-rqCh:
			if rq == nil {
				return nil
			}
			if err := stream.Send(rq); err != nil {
				dlog.Errorf(ctx, "WatchLookupDNS.Send() failed: %v", err)
				return nil
			}
		}
	}
}

// GetLogs acquires the logs for the traffic-manager and/or traffic-agents specified by the
// GetLogsRequest and returns them to the caller
// Deprecated: Clients should use the user daemon's GatherLogs method.
func (s *service) GetLogs(_ context.Context, _ *rpc.GetLogsRequest) (*rpc.LogsResponse, error) {
	return &rpc.LogsResponse{
		PodLogs: make(map[string]string),
		PodYaml: make(map[string]string),
		ErrMsg:  "traffic-manager.GetLogs is deprecated. Please upgrade your telepresence client",
	}, nil
}

func (s *service) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (*empty.Empty, error) {
	s.state.SetTempLogLevel(ctx, request)
	return &empty.Empty{}, nil
}

func (s *service) WatchLogLevel(_ *empty.Empty, stream rpc.Manager_WatchLogLevelServer) error {
	dlog.Debugf(stream.Context(), "WatchLogLevel called")
	return s.state.WaitForTempLogLevel(stream)
}

func (s *service) WatchClusterInfo(session *rpc.SessionInfo, stream rpc.Manager_WatchClusterInfoServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchClusterInfo called")
	return s.clusterInfo.Watch(ctx, stream)
}

const agentSessionTTL = 15 * time.Second

// expire removes stale sessions.
func (s *service) expire(ctx context.Context) {
	now := s.clock.Now()
	s.state.ExpireSessions(ctx, now.Add(-managerutil.GetEnv(ctx).ClientConnectionTTL), now.Add(-agentSessionTTL))
}

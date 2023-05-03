package manager

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	dns2 "github.com/miekg/dns"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/ambassadoragent/cloudtoken"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/cluster"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/license"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
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
	InstallID() string
	RegisterServers(grpcHandler *grpc.Server)
	RunConfigWatcher(context.Context) error
	ServePrometheus(context.Context) error
	RunSessionGCLoop(context.Context) error
	TrafficManagerConfig() []byte
	State() *state.State
}

type service struct {
	ctx           context.Context
	clock         Clock
	ID            string
	state         *state.State
	clusterInfo   cluster.Info
	cloudConfig   *rpc.AmbassadorCloudConfig
	configWatcher config.Watcher
	tokenService  cloudtoken.Service

	rpc.UnsafeManagerServer
}

var _ rpc.ManagerServer = &service{}

type wall struct{}

func (wall) Now() time.Time {
	return time.Now()
}

func getCloudConfig(ctx context.Context) (*rpc.AmbassadorCloudConfig, error) {
	const proxyCertsPath = "/var/run/secrets/proxy_tls"

	env := managerutil.GetEnv(ctx)
	if env.SystemAHost == "" || env.SystemAPort == 0 {
		return nil, nil
	}
	ret := &rpc.AmbassadorCloudConfig{Host: env.SystemAHost, Port: strconv.Itoa(int(env.SystemAPort))} // Why is the port a string?
	if _, err := os.Stat(proxyCertsPath); err != nil {
		if os.IsNotExist(err) {
			return ret, nil
		}
		return nil, fmt.Errorf("could not stat %s: %w", proxyCertsPath, err)
	}
	f, err := os.Open(path.Join(proxyCertsPath, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("unable to to open %s/ca.crt: %w", proxyCertsPath, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("unable to read %s/ca.crt: %w", proxyCertsPath, err)
	}
	ret.ProxyCa = b
	return ret, nil
}

func NewService(ctx context.Context) (Service, context.Context, error) {
	ctx = license.WithBundle(ctx, "/home/telepresence")
	ret := &service{
		clock:        wall{},
		ID:           uuid.New().String(),
		tokenService: cloudtoken.NewPatchConfigmapIfNotPresent(ctx),
	}
	cloudConfig, err := getCloudConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	if cloudConfig != nil && cloudConfig.Host != "" && cloudConfig.Port != "" {
		ret.cloudConfig = cloudConfig
		ctx = a8rcloud.WithSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.UnauthdTrafficManagerConnName, &managerutil.UnauthdConnProvider{Config: cloudConfig})
		ctx = a8rcloud.WithSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName, &ReverseConnProvider{ret})
	}
	ret.configWatcher = config.NewWatcher(managerutil.GetEnv(ctx).ManagerNamespace)
	ret.ctx = ctx
	// These are context dependent so build them once the pool is up
	ret.clusterInfo = cluster.NewInfo(ctx)
	ret.state = state.NewState(ctx)
	return ret, ctx, nil
}

func (m *service) State() *state.State {
	return m.state
}

func (m *service) InstallID() string {
	return m.clusterInfo.GetClusterID()
}

func (m *service) TrafficManagerConfig() []byte {
	return m.configWatcher.GetTrafficManagerConfigYaml()
}

func (m *service) RunConfigWatcher(ctx context.Context) error {
	return m.configWatcher.Run(ctx)
}

// Version returns the version information of the Manager.
func (*service) Version(context.Context, *empty.Empty) (*rpc.VersionInfo2, error) {
	return &rpc.VersionInfo2{Name: DisplayName, Version: version.Version}, nil
}

// GetLicense returns the license for the cluster. This directory is mounted
// via the connector if it detects the presence of a systema license secret
// when installing the traffic-manager.
func (m *service) GetLicense(ctx context.Context, _ *empty.Empty) (*rpc.License, error) {
	resp := rpc.License{
		ClusterId: m.clusterInfo.GetClusterID(),
	}

	lb := license.BundleFromContext(ctx)
	if lb == nil {
		resp.ErrMsg = "license not found"
	} else {
		resp.License = lb.License()
		resp.Host = lb.Host()
	}

	return &resp, nil
}

// CanConnectAmbassadorCloud checks if Ambassador Cloud is resolvable
// from within a cluster.
func (m *service) CanConnectAmbassadorCloud(ctx context.Context, _ *empty.Empty) (*rpc.AmbassadorCloudConnection, error) {
	env := managerutil.GetEnv(ctx)
	if env.SystemAHost == "" || env.SystemAPort == 0 {
		return &rpc.AmbassadorCloudConnection{CanConnect: false}, nil
	}
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", env.SystemAHost, env.SystemAPort), timeout)
	if err != nil {
		dlog.Debugf(ctx, "Failed to connect so assuming in air-gapped environment %s", err)
		return &rpc.AmbassadorCloudConnection{CanConnect: false}, nil
	}
	conn.Close()
	return &rpc.AmbassadorCloudConnection{CanConnect: true}, nil
}

// GetCloudConfig returns the SystemA Host and Port to the caller (currently just used by
// the agents).
func (m *service) GetCloudConfig(ctx context.Context, _ *empty.Empty) (*rpc.AmbassadorCloudConfig, error) {
	if m.cloudConfig == nil {
		return nil, status.Error(codes.Unavailable, "access to Ambassador Cloud is not configured")
	}
	return proto.Clone(m.cloudConfig).(*rpc.AmbassadorCloudConfig), nil
}

// GetTelepresenceAPI returns information about the TelepresenceAPI server.
func (m *service) GetTelepresenceAPI(ctx context.Context, e *empty.Empty) (*rpc.TelepresenceAPIInfo, error) {
	env := managerutil.GetEnv(ctx)
	return &rpc.TelepresenceAPIInfo{Port: int32(env.APIPort)}, nil
}

// ArriveAsClient establishes a session between a client and the Manager.
func (m *service) ArriveAsClient(ctx context.Context, client *rpc.ClientInfo) (*rpc.SessionInfo, error) {
	dlog.Debug(ctx, "ArriveAsClient called")

	if val := validateClient(client); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	sessionID := m.state.AddClient(client, m.clock.Now())
	m.MaybeAddToken(ctx, client.GetApiKey())

	installId := client.GetInstallId()
	return &rpc.SessionInfo{
		SessionId: sessionID,
		ClusterId: m.clusterInfo.GetClusterID(),
		InstallId: &installId,
	}, nil
}

// ArriveAsAgent establishes a session between an agent and the Manager.
func (m *service) ArriveAsAgent(ctx context.Context, agent *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	dlog.Debug(ctx, "ArriveAsAgent called")

	if val := validateAgent(agent); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	sessionID := m.state.AddAgent(agent, m.clock.Now())

	return &rpc.SessionInfo{
		SessionId: sessionID,
		ClusterId: m.clusterInfo.GetClusterID(),
	}, nil
}

func (m *service) GetClientConfig(ctx context.Context, _ *empty.Empty) (*rpc.CLIConfig, error) {
	dlog.Debug(ctx, "GetClientConfig called")

	return &rpc.CLIConfig{
		ConfigYaml: m.configWatcher.GetClientConfigYaml(),
	}, nil
}

// Remain indicates that the session is still valid.
func (m *service) Remain(ctx context.Context, req *rpc.RemainRequest) (*empty.Empty, error) {
	// ctx = WithSessionInfo(ctx, req.GetSession())
	// dlog.Debug(ctx, "Remain called")
	m.MaybeAddToken(ctx, req.GetApiKey())

	if ok := m.state.MarkSession(req, m.clock.Now()); !ok {
		return nil, status.Errorf(codes.NotFound, "Session %q not found", req.GetSession().GetSessionId())
	}

	return &empty.Empty{}, nil
}

// Depart terminates a session.
func (m *service) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, session)
	dlog.Debug(ctx, "Depart called")

	m.state.RemoveSession(ctx, session.GetSessionId())

	return &empty.Empty{}, nil
}

// WatchAgents notifies a client of the set of known Agents.
func (m *service) WatchAgents(session *rpc.SessionInfo, stream rpc.Manager_WatchAgentsServer) error {
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
func (m *service) WatchAgentsNS(request *rpc.AgentsRequest, stream rpc.Manager_WatchAgentsNSServer) error {
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
func (m *service) WatchIntercepts(session *rpc.SessionInfo, stream rpc.Manager_WatchInterceptsServer) error {
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
		if sessionDone, err = m.state.SessionDone(sessionID); err != nil {
			return err
		}

		if agent := m.state.GetAgent(sessionID); agent != nil {
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
		case <-ctx.Done():
			dlog.Debugf(ctx, "WatchIntercepts context cancelled")
			return nil
		case <-sessionDone:
			dlog.Debugf(ctx, "WatchIntercepts session cancelled")
			return nil
		}
	}
}

func (m *service) PrepareIntercept(ctx context.Context, request *rpc.CreateInterceptRequest) (*rpc.PreparedIntercept, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.Session)
	dlog.Debugf(ctx, "PrepareIntercept called")

	span := trace.SpanFromContext(ctx)
	tracing.RecordInterceptSpec(span, request.InterceptSpec)

	return m.state.PrepareIntercept(ctx, request)
}

// CreateIntercept lets a client create an intercept.
func (m *service) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	ctx = managerutil.WithSessionInfo(ctx, ciReq.GetSession())
	sessionID := ciReq.GetSession().GetSessionId()
	spec := ciReq.InterceptSpec
	apiKey := ciReq.GetApiKey()
	dlog.Debug(ctx, "CreateIntercept called")
	span := trace.SpanFromContext(ctx)
	tracing.RecordInterceptSpec(span, spec)

	client := m.state.GetClient(sessionID)

	if client == nil {
		return nil, status.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}

	if val := validateIntercept(spec); val != "" {
		return nil, status.Errorf(codes.InvalidArgument, val)
	}

	interceptInfo, err := m.state.AddIntercept(sessionID, m.clusterInfo.GetClusterID(), apiKey, client, spec)
	if err != nil {
		return nil, err
	}
	if interceptInfo != nil {
		tracing.RecordInterceptInfo(span, interceptInfo)
	}
	if m.cloudConfig == nil {
		return interceptInfo, nil
	}
	err = m.state.AddInterceptFinalizer(interceptInfo.Id, func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error {
		if interceptInfo.ApiKey == "" {
			return nil
		}
		sysa, ok := a8rcloud.GetSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName)
		if !ok {
			return nil
		}
		if sa, err := sysa.Get(ctx); err != nil {
			dlog.Errorln(ctx, "systema: acquire connection:", err)
			return err
		} else {
			dlog.Debugf(ctx, "systema: remove intercept: %q", interceptInfo.Id)
			_, err := sa.RemoveIntercept(ctx, &systema.InterceptRemoval{
				InterceptId: interceptInfo.Id,
			})
			if err != nil {
				return err
			}

			// Release the connection we got to delete the intercept
			if err := sysa.Done(ctx); err != nil {
				dlog.Errorln(ctx, "systema: release management connection:", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return interceptInfo, nil
}

func (m *service) makeinterceptID(_ context.Context, sessionID string, name string) (string, error) {
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

func (m *service) UpdateIntercept(ctx context.Context, req *rpc.UpdateInterceptRequest) (*rpc.InterceptInfo, error) { //nolint:gocognit
	ctx = managerutil.WithSessionInfo(ctx, req.GetSession())
	interceptID, err := m.makeinterceptID(ctx, req.GetSession().GetSessionId(), req.GetName())
	if err != nil {
		return nil, err
	}

	dlog.Debugf(ctx, "UpdateIntercept called: %s", interceptID)

	switch action := req.PreviewDomainAction.(type) {
	case *rpc.UpdateInterceptRequest_AddPreviewDomain:
		// Check if this is already done.
		// Connect to SystemA.
		// Have SystemA create the preview domain.
		// Apply that to the intercept.
		// Oh no, something went wrong.  Clean up.
		intercept, err := m.addInterceptDomain(ctx, interceptID, action)
		if err != nil {
			return nil, err
		}
		return intercept, nil
	case *rpc.UpdateInterceptRequest_RemovePreviewDomain:
		// Check if this is already done.
		// Remove the domain
		intercept, err := m.removeInterceptDomain(ctx, interceptID)
		if err != nil {
			return nil, err
		}
		return intercept, nil
	default:
		panic(fmt.Errorf("unimplemented UpdateInterceptRequest action: %T", action))
	}
}

func (m *service) removeInterceptDomain(ctx context.Context, interceptID string) (*rpc.InterceptInfo, error) {
	var domain string
	systemaPool, ok := a8rcloud.GetSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName)
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "access to Ambassador Cloud is not configured")
	}

	intercept := m.state.UpdateIntercept(interceptID, func(intercept *rpc.InterceptInfo) {
		if intercept.PreviewDomain == "" {
			return
		}

		domain = intercept.PreviewDomain
		intercept.PreviewDomain = ""
	})
	if domain != "" {
		if sa, err := systemaPool.Get(ctx); err != nil {
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
			if err := systemaPool.Done(ctx); err != nil {
				dlog.Errorln(ctx, "systema: release connection:", err)
			}
		}
	}
	if intercept == nil {
		return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", interceptID)
	}
	return intercept, nil
}

func (m *service) addInterceptDomain(ctx context.Context, interceptID string, action *rpc.UpdateInterceptRequest_AddPreviewDomain) (*rpc.InterceptInfo, error) {
	var domain string
	var sa systema.SystemACRUDClient
	systemaPool, ok := a8rcloud.GetSystemAPool[managerutil.SystemaCRUDClient](ctx, a8rcloud.TrafficManagerConnName)
	if !ok {
		return nil, status.Errorf(codes.FailedPrecondition, "access to Ambassador Cloud is not configured")
	}

	var err error
	intercept := m.state.UpdateIntercept(interceptID, func(intercept *rpc.InterceptInfo) {
		if intercept.PreviewDomain != "" {
			return
		}

		if sa == nil {
			sa, err = systemaPool.Get(ctx)
			if err != nil {
				err = fmt.Errorf("systema: acquire connection: %w", err)
				return
			}
		}

		if domain == "" {
			tc, cancel := context.WithTimeout(ctx, systemaCallTimeout)
			defer cancel()
			var resp *systema.CreateDomainResponse
			resp, err = sa.CreateDomain(tc, &systema.CreateDomainRequest{
				InterceptId:       intercept.Id,
				DisplayBanner:     action.AddPreviewDomain.DisplayBanner,
				InterceptSpec:     intercept.Spec,
				Host:              action.AddPreviewDomain.Ingress.L5Host,
				PullRequestUrl:    action.AddPreviewDomain.PullRequestUrl,
				AddRequestHeaders: action.AddPreviewDomain.AddRequestHeaders,
			})
			if err != nil {
				err = status.Errorf(status.Code(err), fmt.Sprintf("systema: create domain: %v", err))
				return
			}
			domain = resp.Domain
		}

		intercept.PreviewDomain = domain
		intercept.PreviewSpec = action.AddPreviewDomain
	})
	if err != nil || intercept == nil || domain == "" || intercept.PreviewDomain != domain {
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
			if err := systemaPool.Done(ctx); err != nil {
				dlog.Errorln(ctx, "systema: release connection:", err)
			}
			sa = nil
		}
	} else if intercept != nil && domain != "" && intercept.PreviewDomain == domain {
		// Everything was created successfully, prep cleanup
		// Note the finalizers will be run in reverse order to how they're added.
		err = m.state.AddInterceptFinalizer(interceptID, func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error {
			// We never dereferenced the systema pool since the reverse connection it initializes must be kept around while the intercept is live.
			if sa != nil {
				if err := systemaPool.Done(ctx); err != nil {
					return fmt.Errorf("systema: release reverse connection: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		err = m.state.AddInterceptFinalizer(interceptID, func(ctx context.Context, interceptInfo *rpc.InterceptInfo) error {
			// Check again for a preview domain in case it was removed separately
			if interceptInfo.PreviewDomain != "" {
				dlog.Debugf(ctx, "systema: removing domain: %q", interceptInfo.PreviewDomain)
				_, err := sa.RemoveDomain(ctx, &systema.RemoveDomainRequest{
					Domain: interceptInfo.PreviewDomain,
				})
				if err != nil {
					return fmt.Errorf("systema: remove domain for intercept %q: %w", interceptID, err)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if intercept == nil {
		err = status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", interceptID)
	}
	return intercept, err
}

// RemoveIntercept lets a client remove an intercept.
func (m *service) RemoveIntercept(ctx context.Context, riReq *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
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

// GetIntercept gets an intercept info from intercept name.
func (m *service) GetIntercept(ctx context.Context, request *rpc.GetInterceptRequest) (*rpc.InterceptInfo, error) {
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
func (m *service) ReviewIntercept(ctx context.Context, rIReq *rpc.ReviewInterceptRequest) (*empty.Empty, error) {
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

func (m *service) Tunnel(server rpc.Manager_TunnelServer) error {
	ctx := server.Context()
	stream, err := tunnel.NewServerStream(ctx, server)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to connect stream: %v", err)
	}
	return m.state.Tunnel(ctx, stream)
}

func (m *service) WatchDial(session *rpc.SessionInfo, stream rpc.Manager_WatchDialServer) error {
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
				// We couldnt stream the dial request. This likely means
				// that we lost connection. Pass lr to the next WatchDial call
				select {
				// If it has been longer than 30s, everyone has probably moved
				// on with their lives.
				case <-time.After(time.Second * 30):
				case lrCh <- lr:
				}
				return nil
			}
		}
	}
}

// LookupHost
// Deprecated: Use LookupDNS
//
//nolint:staticcheck // retained for backward compatibility
func (m *service) LookupHost(ctx context.Context, request *rpc.LookupHostRequest) (*rpc.LookupHostResponse, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSession())
	dlog.Debugf(ctx, "LookupHost %s", request.Name)

	// Use LookupDNS internally
	rsp, err := m.LookupDNS(ctx, &rpc.DNSRequest{
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
func (m *service) AgentLookupHostResponse(ctx context.Context, response *rpc.LookupHostAgentResponse) (*empty.Empty, error) {
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
	m.state.PostLookupDNSResponse(&rpc.DNSAgentResponse{
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
func (m *service) WatchLookupHost(session *rpc.SessionInfo, stream rpc.Manager_WatchLookupHostServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchLookupHost called")
	rqCh := m.state.WatchLookupDNS(session.SessionId)
	for {
		select {
		case <-m.ctx.Done():
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

func (m *service) LookupDNS(ctx context.Context, request *rpc.DNSRequest) (*rpc.DNSResponse, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSession())
	qType := uint16(request.Type)
	qtn := dns2.TypeToString[qType]
	dlog.Debugf(ctx, "LookupDNS %s %s", request.Name, qtn)

	rrs, rCode, err := m.state.AgentsLookupDNS(ctx, request.GetSession().GetSessionId(), request)
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

func (m *service) AgentLookupDNSResponse(ctx context.Context, response *rpc.DNSAgentResponse) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, response.GetSession())
	dlog.Debugf(ctx, "AgentLookupDNSResponse called %s", response.Request.Name)
	m.state.PostLookupDNSResponse(response)
	return &empty.Empty{}, nil
}

func (m *service) WatchLookupDNS(session *rpc.SessionInfo, stream rpc.Manager_WatchLookupDNSServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchLookupDNS called")
	rqCh := m.state.WatchLookupDNS(session.SessionId)
	for {
		select {
		case <-m.ctx.Done():
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
func (m *service) GetLogs(_ context.Context, _ *rpc.GetLogsRequest) (*rpc.LogsResponse, error) {
	return &rpc.LogsResponse{
		PodLogs: make(map[string]string),
		PodYaml: make(map[string]string),
		ErrMsg:  "traffic-manager.GetLogs is deprecated. Please upgrade your telepresence client",
	}, nil
}

func (m *service) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (*empty.Empty, error) {
	m.state.SetTempLogLevel(ctx, request)
	return &empty.Empty{}, nil
}

func (m *service) WatchLogLevel(_ *empty.Empty, stream rpc.Manager_WatchLogLevelServer) error {
	dlog.Debugf(stream.Context(), "WatchLogLevel called")
	return m.state.WaitForTempLogLevel(stream)
}

func (m *service) WatchClusterInfo(session *rpc.SessionInfo, stream rpc.Manager_WatchClusterInfoServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchClusterInfo called")
	return m.clusterInfo.Watch(ctx, stream)
}

const agentSessionTTL = 15 * time.Second

// expire removes stale sessions.
func (m *service) expire(ctx context.Context) {
	now := m.clock.Now()
	m.state.ExpireSessions(ctx, now.Add(-managerutil.GetEnv(ctx).ClientConnectionTTL), now.Add(-agentSessionTTL))
}

// MaybeAddToken maybe adds apikey to the cluster so that the ambassador agent can login.
func (m *service) MaybeAddToken(ctx context.Context, apikey string) {
	if apikey != "" && m.tokenService != nil {
		if err := m.tokenService.MaybeAddToken(ctx, apikey); err != nil {
			dlog.Errorf(ctx, "error creating cloud token: %s", err)
		}
	}
}

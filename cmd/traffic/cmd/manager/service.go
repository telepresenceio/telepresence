package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blang/semver/v4"
	"github.com/google/uuid"
	dns2 "github.com/miekg/dns"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/cluster"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/config"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/mutator"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/quictunnel"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/dnsproxy"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
	maps2 "github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/quicfwd"
	"github.com/telepresenceio/telepresence/v2/pkg/tmconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
	"github.com/telepresenceio/telepresence/v2/pkg/workload"
)

type Service interface {
	rpc.ManagerServer
	ID() string
	InstallID() string
	MakeInterceptID(context.Context, string, string) (string, error)
	RegisterServers(*grpc.Server)
	State() *state.State
	ClusterInfo() cluster.Info

	// unexported methods.
	runUpdateTrafficManagerConfigMapLoop(context.Context) error
	serveHTTP(context.Context) error
	servePrometheus(context.Context) error
	serveQuicTunnel(context.Context) error
	serveX509Auth(context.Context) error
}

type service struct {
	id                 string
	state              *state.State
	clusterInfo        cluster.Info
	configWatcher      config.Watcher
	authorizer         *auth.Authorizer
	authMode           auth.Mode
	activeHttpRequests int32
	activeGrpcRequests int32
	serviceNameNs      string
	serviceNameFQN     string
	dotClusterDomain   string
	tmConfigMapUpdated atomic.Bool

	// quicCA is always non-nil: it backs both the QUIC tunnel's mTLS trust (gated
	// separately by the QUIC listener) and the session credential minted by
	// GetSessionCredential, which works regardless of whether QUIC is enabled. It is
	// generated once in NewService and never persisted; a manager restart mints a
	// new CA and implicitly revokes every client certificate and session token the
	// previous one signed.
	quicCA *quictunnel.CA

	// quicDiscovery is non-nil only when the QUIC tunnel listener is enabled AND no
	// explicit TunnelQuicExternalHost override is configured: the override replaces
	// discovery entirely (see GetQuicTunnelEndpoint), so there is nothing for it to
	// do. quicCandidates checks for nil before calling Candidates.
	quicDiscovery *quictunnel.Discovery

	// mintedTokens holds the bearer tokens the x509 auth listener has issued, shared
	// with the Authenticator constructed in serveHTTP so a token minted there is
	// accepted on the regular gRPC channel. It exists regardless of whether the
	// listener is enabled; an always-empty store is harmless.
	mintedTokens *auth.MintedTokens

	// x509ClientCA is non-nil only when AUTH_X509_PORT != 0 and authMode is
	// enforcing, so that a manually set port can't add attack surface in a
	// permissive cluster.
	x509ClientCA *auth.ClientCAPool

	// x509Listener is bound in NewService, before the gRPC server starts, so that
	// Version never advertises the auth port until the listener accepts connections.
	// Non-nil exactly when x509ClientCA is.
	x509Listener *auth.X509Listener

	rpc.UnsafeManagerServer
}

var _ rpc.ManagerServer = &service{}

// checkCompat checks if a CompatibilityVersion has been set for this traffic-manager, and if so, errors with
// an Unimplemented error mentioning the given name if it is less than the required version.
func checkCompat(ctx context.Context, name, requiredVersion string) error {
	if cv := managerutil.GetEnv(ctx).CompatibilityVersion; cv != nil && cv.Compare(semver.MustParse(requiredVersion)) < 0 {
		return status.Error(codes.Unimplemented, fmt.Sprintf("traffic manager of version %s does not implement %s", cv, name))
	}
	return nil
}

func NewService(ctx context.Context, g log.Group, configWatcher config.Watcher) (Service, error) {
	ret := &service{
		id:            uuid.New().String(),
		configWatcher: configWatcher,
		authorizer:    auth.NewAuthorizer(k8sapi.GetK8sInterface(ctx)),
		mintedTokens:  auth.NewMintedTokens(),
	}

	// These are context-dependent, so build them once the pool is up
	var err error
	ret.clusterInfo, err = cluster.NewInfo(ctx)
	if err != nil {
		clog.Errorf(ctx, "unable to initialize cluster info: %v", err)
		return nil, err
	}
	env := managerutil.GetEnv(ctx)
	ns := env.ManagerNamespace
	ret.authMode = env.AuthenticationMode
	ret.dotClusterDomain = "." + ret.clusterInfo.ClusterDomain()
	ret.serviceNameNs = fmt.Sprintf("%s.%s.", agentconfig.ManagerAppName, ns)
	ret.serviceNameFQN = fmt.Sprintf("%s.%s.svc%s", agentconfig.ManagerAppName, ns, ret.dotClusterDomain)

	ret.quicCA, err = quictunnel.NewCA()
	if err != nil {
		clog.Errorf(ctx, "unable to initialize QUIC tunnel CA: %v", err)
		return nil, err
	}
	if env.TunnelQuicPort != 0 {
		// An explicit externalHost bypasses discovery entirely (see
		// GetQuicTunnelEndpoint), so there is no reason to start it.
		if env.TunnelQuicExternalHost == "" && env.TunnelQuicServiceName != "" {
			ret.quicDiscovery = quictunnel.NewDiscovery()
			ret.quicDiscovery.Start(ctx, ns, env.TunnelQuicServiceName)
		}
	}

	if env.AuthX509Port != 0 && ret.authMode == auth.ModeEnforcing {
		ret.x509ClientCA = auth.NewClientCAPool(ctx, k8sapi.GetK8sInterface(ctx))
		ret.x509ClientCA.OnChange(ret.mintedTokens.InvalidateAll)
		// OnChange only fires on a later reload, so record the initial load's
		// generation (0 if it failed) here.
		_, generation := ret.x509ClientCA.Snapshot()
		ret.mintedTokens.InvalidateAll(generation)
		ret.x509Listener, err = auth.NewX509Listener(env.AuthX509Port, ret.x509ClientCA, ret.mintedTokens)
		if err != nil {
			clog.Errorf(ctx, "unable to start x509 auth listener: %v", err)
			return nil, err
		}
	}

	ret.state = state.NewState(ctx, g, configWatcher.AdminCommandChannel())
	return ret, nil
}

func (s *service) ClusterInfo() cluster.Info {
	return s.clusterInfo
}

func (s *service) ID() string {
	return s.id
}

func (s *service) State() *state.State {
	return s.state
}

func (s *service) InstallID() string {
	return s.clusterInfo.ID()
}

// Version returns the version information of the Manager.
func (s *service) Version(ctx context.Context, _ *empty.Empty) (*rpc.VersionInfo2, error) {
	vi := &rpc.VersionInfo2{
		Name:          DisplayName,
		Version:       version.Version,
		AuthSupported: s.authMode != auth.ModeDisabled,
		AuthRequired:  s.authMode == auth.ModeEnforcing,
	}
	// The port is advertised only when the listener is up and accepting
	// connections, which NewService guarantees by binding it before any server
	// starts.
	if s.x509Listener != nil {
		vi.AuthX509Port = uint32(managerutil.GetEnv(ctx).AuthX509Port)
	}
	return vi, nil
}

func (s *service) GetAgentImageFQN(ctx context.Context, _ *empty.Empty) (*rpc.AgentImageFQN, error) {
	if managerutil.AgentInjectorEnabled(ctx) {
		return &rpc.AgentImageFQN{
			FQN: managerutil.GetAgentImage(ctx),
		}, nil
	}
	return nil, status.Error(codes.Unavailable, "")
}

func (s *service) GetAgentConfig(ctx context.Context, request *rpc.AgentConfigRequest) (*rpc.AgentConfigResponse, error) {
	ctx, clientInfo, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return nil, err
	}
	namespace, err := s.managedTargetNamespace(ctx, clientInfo, request.Namespace)
	if err != nil {
		return nil, err
	}
	scs, err := s.State().GetOrGenerateAgentConfig(ctx, request.Name, namespace)
	if err != nil {
		return nil, err
	}
	r := rpc.AgentConfigResponse{}
	r.Data, err = scs.Marshal()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &r, nil
}

func (s *service) managedTargetNamespace(ctx context.Context, clientInfo *state.ClientSession, namespace string) (string, error) {
	if namespace == "" {
		namespace = clientInfo.Namespace
	}
	if !s.State().ManagesNamespace(ctx, namespace) {
		return "", status.Errorf(codes.FailedPrecondition, "namespace %s is not managed by this traffic-manager", namespace)
	}
	return namespace, nil
}

// GetTelepresenceAPI returns information about the TelepresenceAPI server.
func (s *service) GetTelepresenceAPI(ctx context.Context, e *empty.Empty) (*rpc.TelepresenceAPIInfo, error) {
	env := managerutil.GetEnv(ctx)
	return &rpc.TelepresenceAPIInfo{Port: int32(env.AgentRestApiPort)}, nil
}

// ArriveAsClient establishes a session between a client and the Manager.
func (s *service) ArriveAsClient(ctx context.Context, client *rpc.ClientInfo) (*rpc.SessionInfo, error) {
	clog.Debugf(ctx, "Namespace: %s", client.Namespace)

	if !s.State().ManagesNamespace(ctx, client.Namespace) {
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("namespace %s is not managed", client.Namespace))
	}

	if val := validateClient(client); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	installId := client.GetInstallId()

	IncrementCounter(ctx, s.state.GetConnectCounter(), client.Name, client.InstallId)
	SetGauge(ctx, s.state.GetConnectActiveStatus(), client.Name, client.InstallId, nil, 1)

	return &rpc.SessionInfo{
		SessionId:        string(s.state.AddClient(client, auth.PrincipalFrom(ctx), time.Now())),
		ManagerInstallId: s.clusterInfo.ID(),
		InstallId:        &installId,
	}, nil
}

func (s *service) ReconnectClient(ctx context.Context, info *rpc.ReconnectClientRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, info.Session)
	sessionID := tunnel.SessionID(info.GetSession().GetSessionId())
	if session := s.state.GetClient(sessionID); session != nil {
		if err := state.ClientOwnershipError(ctx, sessionID, session); err != nil {
			return nil, err
		}
		// We already know this client, so we don't need to do anything.
		return &empty.Empty{}, nil
	}
	client := info.Client
	st := s.state
	if !st.ManagesNamespace(ctx, client.Namespace) {
		// Sorry, we no longer manage this namespace.
		return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("namespace %s is not managed", client.Namespace))
	}
	if val := validateClient(client); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}
	now := time.Now()
	st.RestoreClient(sessionID, client, auth.PrincipalFrom(ctx), now)
	agents := slices.DeleteFunc(slices.Clone(info.Agents), func(agent *rpc.AgentInfo) bool {
		if st.ManagesNamespace(ctx, agent.Namespace) {
			return false
		}
		clog.Debugf(ctx, "Not restoring agent %s.%s because its namespace is not managed", agent.Name, agent.Namespace)
		return true
	})
	intercepts := slices.DeleteFunc(slices.Clone(info.Intercepts), func(intercept *rpc.InterceptInfo) bool {
		spec := intercept.GetSpec()
		if spec != nil && st.ManagesNamespace(ctx, spec.Namespace) {
			return false
		}
		clog.Debugf(ctx, "Not restoring intercept %s because its namespace is not managed", intercept.GetId())
		return true
	})
	st.RestoreAgents(agents, now)
	st.RestoreIntercepts(ctx, intercepts, now)
	return &empty.Empty{}, nil
}

// ArriveAsAgent establishes a session between an agent and the Manager.
func (s *service) ArriveAsAgent(ctx context.Context, agent *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	clog.Debugf(ctx, "Name %s, IP %s", agent.PodName, agent.PodIp)
	if val := validateAgent(agent); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	for _, cn := range agent.Containers {
		s.removeExcludedEnvVars(cn.Environment)
	}

	principal, mismatch := verifiedAgentPrincipal(ctx, agent)
	if mismatch && s.authMode == auth.ModeEnforcing {
		return nil, errors.Errorf(codes.PermissionDenied,
			"bound token does not match the presented agent identity %s.%s", agent.PodName, agent.Namespace)
	}
	sessionID, err := s.state.AddAgent(ctx, agent, principal, time.Now())
	if err != nil {
		return nil, err
	}

	return &rpc.SessionInfo{
		SessionId:        string(sessionID),
		ManagerInstallId: s.clusterInfo.ID(),
	}, nil
}

func (s *service) ReconnectAgent(ctx context.Context, rq *rpc.ReconnectAgentRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, rq.Session)
	sessionID := tunnel.SessionID(rq.GetSession().SessionId)
	if _, _, err := s.ensureAgentSession(ctx, rq.Session); err != nil && status.Code(err) != codes.NotFound {
		return nil, err
	}
	principal, mismatch := verifiedAgentPrincipal(ctx, rq.Agent)
	if mismatch && s.authMode == auth.ModeEnforcing {
		return nil, errors.Errorf(codes.PermissionDenied,
			"bound token does not match the presented agent identity %s.%s", rq.Agent.PodName, rq.Agent.Namespace)
	}
	_, err := s.state.RestoreAgent(ctx, sessionID, rq.Agent, principal, time.Now())
	return &empty.Empty{}, err
}

// verifiedAgentPrincipal returns the caller's principal when its bound-token pod
// claims match the presented AgentInfo, and whether a presented principal failed
// to match (mismatch). mismatch is always false when the caller had no principal
// at all -- an old, tokenless agent -- which is a distinct, permitted case.
func verifiedAgentPrincipal(ctx context.Context, agent *rpc.AgentInfo) (principal *auth.Principal, mismatch bool) {
	p := auth.PrincipalFrom(ctx)
	if p == nil {
		clog.Debugf(ctx, "agent %s.%s arrived without a bound token", agent.PodName, agent.Namespace)
		return nil, false
	}
	if p.PodName == agent.PodName && p.PodUID == agent.PodUid && strings.HasPrefix(p.Username, "system:serviceaccount:"+agent.Namespace+":") {
		return p, false
	}
	clog.Warnf(ctx, "bound token for pod %s (uid %s) does not match presented agent identity %s.%s (uid %s); not binding the session",
		p.PodName, p.PodUID, agent.PodName, agent.Namespace, agent.PodUid)
	return nil, true
}

// agentOwnershipError returns a PermissionDenied error when agent is bound to a
// verified pod identity and the caller's principal doesn't match it. Returns nil
// when the session is unbound (old agent) or the caller is the bound pod itself.
// A caller whose token couldn't be verified for infrastructure reasons gets
// Unavailable instead, since ownership could not be established either way.
func agentOwnershipError(ctx context.Context, sessionID tunnel.SessionID, agent *state.AgentSession) error {
	bound := agent.Principal()
	if bound == nil {
		return nil
	}
	if p := auth.PrincipalFrom(ctx); p != nil && p.PodUID == bound.PodUID && p.PodName == bound.PodName {
		return nil
	}
	if auth.AuthUnavailable(ctx) {
		return errors.Errorf(codes.Unavailable, "cannot verify session ownership: authentication unavailable")
	}
	return errors.Errorf(codes.PermissionDenied, "agent session %q is bound to another workload identity", sessionID)
}

func (s *service) ReportMetrics(ctx context.Context, metrics *rpc.TunnelMetrics) (*empty.Empty, error) {
	s.state.AddSessionConsumptionMetrics(metrics)
	return &empty.Empty{}, nil
}

func (s *service) GetClientConfig(ctx context.Context, _ *empty.Empty) (*rpc.CLIConfig, error) {
	return &rpc.CLIConfig{
		ConfigYaml: s.configWatcher.GetClientConfigYaml(ctx),
	}, nil
}

// Remain indicates that the session is still valid.
func (s *service) Remain(ctx context.Context, req *rpc.RemainRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, req.GetSession())
	sessionID := managerutil.GetSessionID(ctx)
	if agent := s.state.GetAgent(sessionID); agent != nil {
		if err := agentOwnershipError(ctx, sessionID, agent); err != nil {
			return nil, err
		}

		agent.Mark(time.Now())
		workloadKey := &mutator.WorkloadKey{
			Name:      agent.Name,
			Namespace: agent.Namespace,
			Kind:      k8sapi.Kind(agent.Kind),
		}

		err := s.UpdateLastAttachmentTime(ctx, workloadKey)
		if err != nil {
			clog.Errorf(ctx, "error updating last attachment time: %v", err)
		}
		err = s.removeUnusedAgent(ctx, workloadKey)
		if err != nil {
			clog.Errorf(ctx, "error removing unused agent: %v", err)
		}

		return &empty.Empty{}, err
	}

	client := s.state.GetClient(sessionID)
	if client == nil {
		return nil, status.Errorf(codes.NotFound, "Session %q not found", sessionID)
	}
	if err := state.ClientOwnershipError(ctx, sessionID, client); err != nil {
		return nil, err
	}
	var lastActivity time.Time
	if la := req.LastActivity; la != nil {
		lastActivity = la.AsTime()
	} else {
		lastActivity = time.Now()
	}
	if client.Mark(lastActivity) {
		clog.Tracef(ctx, "Last activity: %s", lastActivity)
	}
	client.ConsumptionMetrics().AddTimeSpent()
	return &empty.Empty{}, nil
}

// Depart terminates a session.
func (s *service) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, session)

	sessionID := tunnel.SessionID(session.GetSessionId())
	if agent := s.state.GetAgent(sessionID); agent != nil {
		if err := agentOwnershipError(ctx, sessionID, agent); err != nil {
			return nil, err
		}
	} else if client := s.state.GetClient(sessionID); client != nil {
		if err := state.ClientOwnershipError(ctx, sessionID, client); err != nil {
			return nil, err
		}
	}
	// There's no reason for the caller to wait for this removal to complete.
	go s.state.RemoveSession(context.WithoutCancel(ctx), sessionID)
	return &empty.Empty{}, nil
}

func (s *service) removeUnusedAgent(ctx context.Context, workloadKey *mutator.WorkloadKey) error {
	maxIdleTime := managerutil.GetEnv(ctx).AgentMaxIdleTime
	if maxIdleTime == 0 {
		// default aka not set is 0, we don't ever remove agents, skip
		return nil
	}

	activeIntercepts := s.state.CountActiveInterceptsForWorkload(workloadKey)
	if activeIntercepts != 0 {
		// don't remove if there are active intercepts
		return nil
	}

	agentStateFileYAML := s.configWatcher.GetAgentStateYaml(ctx)
	var agentStateFile AgentStateFile
	err := yaml.Unmarshal(agentStateFileYAML, &agentStateFile)
	if err != nil {
		return fmt.Errorf("err in unmarshalling YAML: %w", err)
	}

	agentState, ok := agentStateFile.AgentStates[*workloadKey]
	if !ok {
		return fmt.Errorf("error in looking up workload key %s in agentStateFile, err: %w", workloadKey, err)
	}
	lastAttachmentTime := agentState.lastAttachment()
	clog.Tracef(ctx, "Last attachment time for agent %s: %s", *workloadKey, lastAttachmentTime)
	if lastAttachmentTime.IsZero() {
		// means it was never attached
		return nil
	}
	idleTime := time.Since(lastAttachmentTime)
	clog.Tracef(ctx, "Idle time for agent %s: %s", *workloadKey, idleTime)
	if idleTime > maxIdleTime {
		// construct uninstall agents request
		clog.Infof(ctx, "Removing agent %s due to idle time %s exceeding max idle time %s", *workloadKey, idleTime, maxIdleTime)
		ns := workloadKey.Namespace
		mm := mutator.GetMap(ctx)
		wl, err := k8sapi.GetWorkload(ctx, workloadKey.Name, ns, "")
		if err != nil {
			return errors.Errorf(codes.NotFound, "Workload %s.%s not found", workloadKey.Name, ns)
		}
		mm.Delete(wl.GetName(), ns)
		if err := mm.EvictPodsWithAgentConfig(ctx, wl); err != nil {
			return errors.Errorf(codes.Internal, "unable to delete agent for workload %s.%s: %v", wl.GetName(), ns, err)
		}
	}
	return nil
}

func (s *service) UpdateLastAttachmentTime(ctx context.Context, workloadKey *mutator.WorkloadKey) error {
	// updates last attachment time IN MEMORY, this is persisted to the configmap by another goroutine that runs periodically
	clog.Tracef(ctx, "Logging workloadKey for last attachment time: %s", *workloadKey)

	agentStateFileYAML := s.configWatcher.GetAgentStateYaml(ctx)
	clog.Tracef(ctx, "Logging agentStateFileYAML: %s", agentStateFileYAML)

	var agentStateFile AgentStateFile
	if string(agentStateFileYAML) != "" {
		clog.Tracef(ctx, "Unmarshalling agent states from YAML")
		err := yaml.Unmarshal(agentStateFileYAML, &agentStateFile)
		if err != nil {
			return fmt.Errorf("error unmarshalling agent states: %w", err)
		}
		if !agentStateFile.AgentStates[*workloadKey].lastAttachment().IsZero() && s.state.CountActiveInterceptsForWorkload(workloadKey) == 0 {
			// don't update last attachment time if there are no active intercepts and it is not the first time
			return nil
		}
	} else {
		agentStateFile = AgentStateFile{AgentStates: make(map[mutator.WorkloadKey]AgentState)}
	}

	agentStateFile.AgentStates[*workloadKey] = AgentState{LastAttachmentTime: time.Now()}

	updatedAgentStateFileYAML, err := yaml.Marshal(agentStateFile)
	if err != nil {
		return fmt.Errorf("error marshalling agent states: %w", err)
	}
	s.configWatcher.SetAgentStateYaml(ctx, updatedAgentStateFileYAML)
	s.tmConfigMapUpdated.Store(true)
	return nil
}

type AgentState struct {
	LastAttachmentTime time.Time `json:"lastAttachmentTime"`

	// LegacyLastEngagementTime is the timestamp written by managers that predate
	// the attachment terminology. It is only read, never written.
	LegacyLastEngagementTime time.Time `json:"lastEngagementTime,omitempty"`
}

// lastAttachment returns LastAttachmentTime, falling back to the timestamp
// written under the pre-attachment key.
func (a AgentState) lastAttachment() time.Time {
	if a.LastAttachmentTime.IsZero() {
		return a.LegacyLastEngagementTime
	}
	return a.LastAttachmentTime
}

type AgentStateFile struct {
	AgentStates map[mutator.WorkloadKey]AgentState `json:"agentStates"`
}

func (f AgentStateFile) MarshalJSON() ([]byte, error) {
	temp := make(map[string]AgentState)
	for k, v := range f.AgentStates {
		temp[k.String()] = v
	}
	return json.Marshal(map[string]interface{}{
		"agentStates": temp,
	})
}

func (f *AgentStateFile) UnmarshalJSON(data []byte) error {
	var raw struct {
		AgentStates map[string]AgentState `json:"agentStates"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	f.AgentStates = make(map[mutator.WorkloadKey]AgentState)
	for keyStr, val := range raw.AgentStates {
		parts := strings.Split(keyStr, ".")
		if len(parts) != 3 {
			return fmt.Errorf("invalid key format: %s", keyStr)
		}
		f.AgentStates[mutator.WorkloadKey{
			Kind:      k8sapi.Kind(parts[0]),
			Name:      parts[1],
			Namespace: parts[2],
		}] = val
	}
	return nil
}

func (s *service) agentPodNamespaces(ctx context.Context, clientInfo *state.ClientSession, namespaces []string) ([]string, error) {
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}
	nss := make([]string, 0, len(namespaces))
	for _, namespace := range namespaces {
		namespace, err := s.managedTargetNamespace(ctx, clientInfo, namespace)
		if err != nil {
			return nil, err
		}
		nss = append(nss, namespace)
	}
	sort.Strings(nss)
	return slices.Compact(nss), nil
}

func (s *service) createAgentPodWatchers(ctx context.Context, namespaces []string) (
	<-chan cache.Delta[tunnel.SessionID, *state.AgentSession],
	<-chan cache.Delta[string, *state.Intercept],
	<-chan struct{},
) {
	nsSet := make(map[string]struct{}, len(namespaces))
	for _, ns := range namespaces {
		nsSet[ns] = struct{}{}
	}
	agentsCh := s.state.WatchAgents(ctx, func(_ tunnel.SessionID, info *state.AgentSession) bool {
		_, ok := nsSet[info.Namespace]
		return ok
	})
	sessionID := managerutil.GetSessionID(ctx)
	interceptsCh := s.state.WatchIntercepts(ctx, func(_ string, info *state.Intercept) bool {
		return info.ClientSession.SessionId == string(sessionID)
	})
	sessionDone, _ := s.state.SessionDone(sessionID)
	return agentsCh, interceptsCh, sessionDone
}

// WatchAgentPods notifies a client of the set of known Agents.
func (s *service) WatchAgentPods(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentPodInfoSnapshot]) error {
	ctx, clientInfo, err := s.ensureClientSession(stream.Context(), session)
	if err != nil {
		return err
	}
	namespaces, err := s.agentPodNamespaces(ctx, clientInfo, nil)
	if err != nil {
		return err
	}
	return s.watchAgentPods(ctx, namespaces, stream)
}

func (s *service) watchAgentPods(ctx context.Context, namespaces []string, stream grpc.ServerStreamingServer[rpc.AgentPodInfoSnapshot]) error {
	clientSessionID := managerutil.GetSessionID(ctx)
	agentsCh, interceptsCh, sessionDone := s.createAgentPodWatchers(ctx, namespaces)
	agentSessions := cache.NewClientMap[tunnel.SessionID, *state.AgentSession]()

	interceptInfos := cache.NewClientMap[string, *state.Intercept]()
	lock := sync.Mutex{}
	var lastAgents []*rpc.AgentPodInfo

	onChanged := func() (err error) {
		lock.Lock()
		defer lock.Unlock()
		m := mutator.GetMap(ctx)
		agents := make([]*rpc.AgentPodInfo, 0, agentSessions.Size())
		agentSessions.Range(func(id tunnel.SessionID, a *state.AgentSession) bool {
			if m.IsInactive(types.UID(a.PodUid)) {
				return true
			}
			aip, parseErr := netip.ParseAddr(a.PodIp)
			if parseErr != nil {
				clog.Errorf(ctx, "error parsing agent pod ip %q: %v", a.PodIp, parseErr)
			}
			ap := &rpc.AgentPodInfo{
				WorkloadName: a.Name,
				PodId:        a.PodUid,
				PodName:      a.PodName,
				Namespace:    a.Namespace,
				PodIp:        aip.AsSlice(),
				ApiPort:      a.ApiPort,
				Intercepted:  s.state.IsInterceptedBy(a, clientSessionID),
				NodeAgent:    a.NodeAgent,
				QuicSni:      quicSNIForAgent(a.QuicPort, a.PodUid),
			}
			agents = append(agents, ap)
			return true
		})
		if !slices.Equal(lastAgents, agents) {
			lastAgents = agents
			err = stream.Send(&rpc.AgentPodInfoSnapshot{Agents: agents})
		}
		return err
	}

	go func() {
		_ = interceptInfos.Watch(sessionDone, interceptsCh, onChanged)
	}()
	return agentSessions.Watch(sessionDone, agentsCh, onChanged)
}

func (s *service) WatchAgentPodsDelta(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentPodInfoDelta]) error {
	ctx := stream.Context()
	if err := checkCompat(ctx, "WatchAgentPodsDelta", "2.26.0"); err != nil {
		return err
	}
	ctx, clientInfo, err := s.ensureClientSession(ctx, session)
	if err != nil {
		return err
	}
	namespaces, err := s.agentPodNamespaces(ctx, clientInfo, nil)
	if err != nil {
		return err
	}
	return s.watchAgentPodsDelta(ctx, namespaces, stream)
}

func (s *service) WatchAgentPodsInNamespacesDelta(request *rpc.AgentsRequest, stream grpc.ServerStreamingServer[rpc.AgentPodInfoDelta]) error {
	ctx := stream.Context()
	if err := checkCompat(ctx, "WatchAgentPodsInNamespacesDelta", "2.28.0"); err != nil {
		return err
	}
	ctx, clientInfo, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return err
	}
	namespaces, err := s.agentPodNamespaces(ctx, clientInfo, request.Namespaces)
	if err != nil {
		return err
	}
	return s.watchAgentPodsDelta(ctx, namespaces, stream)
}

// agentPodProjection folds AgentSession deltas into a debounced, deduped map
// of rpc.AgentPodInfo -- the projection shared by watchAgentPodsDelta and
// WatchSessionEvents: inactive-pod filtering (mutator.Map), the per-client
// Intercepted flag (state.IsInterceptedBy) and QuicSni (quicSNIForAgent).
type agentPodProjection struct {
	s               *service
	clientSessionID tunnel.SessionID
	m               mutator.Map
	agentPodInfos   *cache.Map[string, *rpc.AgentPodInfo]
}

func (s *service) newAgentPodProjection(ctx context.Context, clientSessionID tunnel.SessionID) *agentPodProjection {
	return &agentPodProjection{
		s:               s,
		clientSessionID: clientSessionID,
		m:               mutator.GetMap(ctx),
		agentPodInfos: cache.NewMap[string, *rpc.AgentPodInfo](func(a *rpc.AgentPodInfo, b *rpc.AgentPodInfo) bool {
			return proto.Equal(a, b)
		}, time.Millisecond),
	}
}

// run starts the goroutine that folds agentsCh deltas into the projection.
// It exits when ctx is done or sessionDone is closed.
func (p *agentPodProjection) run(ctx context.Context, sessionDone <-chan struct{}, agentsCh <-chan cache.Delta[tunnel.SessionID, *state.AgentSession]) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-sessionDone:
				return
			case delta := <-agentsCh:
				for k, a := range delta.Upserts {
					if p.m.IsInactive(types.UID(a.PodUid)) {
						continue
					}
					aip, parseErr := netip.ParseAddr(a.PodIp)
					if parseErr != nil {
						clog.Errorf(ctx, "error parsing agent pod ip %q: %v", a.PodIp, parseErr)
						continue
					}
					ap := &rpc.AgentPodInfo{
						WorkloadName: a.Name,
						PodId:        a.PodUid,
						PodName:      a.PodName,
						Namespace:    a.Namespace,
						PodIp:        aip.AsSlice(),
						ApiPort:      a.ApiPort,
						Intercepted:  p.s.state.IsInterceptedBy(a, p.clientSessionID),
						NodeAgent:    a.NodeAgent,
						QuicSni:      quicSNIForAgent(a.QuicPort, a.PodUid),
						Version:      a.Version,
					}
					p.agentPodInfos.Store(string(k), ap)
				}
				for k := range delta.Removals {
					p.agentPodInfos.Delete(string(k))
				}
			}
		}
	}()
}

// refreshIntercepted recomputes the Intercepted flag for every projected
// pod, updating (and thereby re-notifying) only the ones whose flag
// actually flipped.
func (p *agentPodProjection) refreshIntercepted() {
	p.agentPodInfos.Range(func(k string, a *rpc.AgentPodInfo) bool {
		if p.m.IsInactive(types.UID(a.PodId)) {
			return true
		}
		agent := p.s.state.GetAgent(tunnel.SessionID(state.AgentSessionIDPrefix + a.PodId))
		intercepted := p.s.state.IsInterceptedBy(agent, p.clientSessionID)
		p.agentPodInfos.Compute(k, func(a *rpc.AgentPodInfo, loaded bool) (*rpc.AgentPodInfo, xsync.ComputeOp) {
			if loaded && a.Intercepted != intercepted {
				a := proto.Clone(a).(*rpc.AgentPodInfo)
				a.Intercepted = intercepted
				return a, xsync.UpdateOp
			}
			return a, xsync.CancelOp
		})
		return true
	})
}

// subscribe returns the channel of deltas produced by the projection.
func (p *agentPodProjection) subscribe(done <-chan struct{}) <-chan cache.Delta[string, *rpc.AgentPodInfo] {
	return p.agentPodInfos.Subscribe(done, nil)
}

func (s *service) watchAgentPodsDelta(ctx context.Context, namespaces []string, stream grpc.ServerStreamingServer[rpc.AgentPodInfoDelta]) error {
	clientSessionID := managerutil.GetSessionID(ctx)
	agentsCh, interceptsCh, sessionDone := s.createAgentPodWatchers(ctx, namespaces)
	proj := s.newAgentPodProjection(ctx, clientSessionID)
	proj.run(ctx, sessionDone, agentsCh)

	agentPodInfosCh := proj.subscribe(ctx.Done())
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sessionDone:
			return nil
		case <-interceptsCh:
			proj.refreshIntercepted()
		case delta := <-agentPodInfosCh:
			if err := stream.Send(&rpc.AgentPodInfoDelta{Upserts: delta.Upserts, Removals: maps2.KeySlice(delta.Removals)}); err != nil {
				return err
			}
		}
	}
}

// WatchAgents notifies a client of the set of known Agents in the connected namespace.
func (s *service) WatchAgents(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentInfoSnapshot]) error {
	ctx, clientInfo, err := s.ensureClientSession(stream.Context(), session)
	if err != nil {
		return err
	}
	ns := clientInfo.Namespace
	return s.watchAgents(ctx, func(_ tunnel.SessionID, a *state.AgentSession) bool { return a.Namespace == ns }, stream)
}

func infosEqual(a, b *rpc.AgentInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	return proto.Equal(a, b)
}

func (s *service) watchAgents(ctx context.Context, includeAgent func(tunnel.SessionID, *state.AgentSession) bool, stream grpc.ServerStreamingServer[rpc.AgentInfoSnapshot]) error {
	deltaCh := s.state.WatchAgents(ctx, includeAgent)
	sessionDone, err := s.state.SessionDone(managerutil.GetSessionID(ctx))
	if err != nil {
		return err
	}
	snapshot := cache.NewClientMap[tunnel.SessionID, *state.AgentSession]()
	// Ensure that the initial snapshot is not equal to lastSnap even if it is empty by
	// creating a lastSnap with one nil entry.
	var lastSnap []*rpc.AgentInfo
	firstSnap := true

	return snapshot.Watch(sessionDone, deltaCh, func() error {
		m := mutator.GetMap(ctx)

		// Sort snapshot by sessionID and discard inactive agents.
		agentSessionIDs := make([]tunnel.SessionID, 0, snapshot.Size())
		snapshot.Range(func(id tunnel.SessionID, _ *state.AgentSession) bool {
			agentSessionIDs = append(agentSessionIDs, id)
			return true
		})
		slices.Sort(agentSessionIDs)
		agents := make([]*rpc.AgentInfo, 0, len(agentSessionIDs))
		for _, agentSessionID := range agentSessionIDs {
			ag, ok := snapshot.Load(agentSessionID)
			if ok && !m.IsInactive(types.UID(ag.PodUid)) {
				agents = append(agents, ag.AgentInfo)
			}
		}
		if firstSnap {
			firstSnap = false
		} else if slices.EqualFunc(agents, lastSnap, infosEqual) {
			return nil
		}
		lastSnap = agents
		if clog.Enabled(ctx, slog.LevelDebug) {
			names := make([]string, len(agents))
			i := 0
			for _, a := range agents {
				names[i] = a.PodName + "." + a.Namespace
				i++
			}
			clog.Debugf(ctx, "Sending update %v", names)
		}
		resp := &rpc.AgentInfoSnapshot{
			Agents: agents,
		}
		return stream.Send(resp)
	})
}

func (s *service) WatchAgentsDelta(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentInfoDelta]) error {
	ctx := stream.Context()
	if err := checkCompat(ctx, "WatchAgentsDelta", "2.26.0"); err != nil {
		return err
	}
	ctx, clientInfo, err := s.ensureClientSession(ctx, session)
	if err != nil {
		return err
	}
	ns := clientInfo.Namespace
	deltaCh := s.state.WatchAgents(ctx, func(_ tunnel.SessionID, a *state.AgentSession) bool { return a.Namespace == ns })
	sessionDone, err := s.state.SessionDone(managerutil.GetSessionID(ctx))
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sessionDone:
			return nil
		case delta := <-deltaCh:
			aid := rpc.AgentInfoDelta{}
			if rl := len(delta.Upserts); rl > 0 {
				aid.Upserts = make(map[string]*rpc.AgentInfo, rl)
				for k, v := range delta.Upserts {
					aid.Upserts[string(k)] = v.AgentInfo
				}
			}
			if rl := len(delta.Removals); rl > 0 {
				aid.Removals = make([]string, rl)
				for k := range delta.Removals {
					rl--
					aid.Removals[rl] = string(k)
				}
			}
			clog.Debugf(ctx, "Sending %d upserts and %d removals", len(aid.Upserts), len(aid.Removals))
			err = stream.Send(&aid)
			if err != nil {
				return err
			}
		}
	}
}

// filterInterceptDeltas keeps a per-subscriber view of matching intercepts.
// Unlike cache.Map's stateless filter, it emits a removal when an updated
// intercept stops matching, which is required when a Service selector prunes
// a participant that previously received the intercept.
func filterInterceptDeltas(
	ctx context.Context,
	source <-chan cache.Delta[string, *state.Intercept],
	include func(string, *state.Intercept) bool,
) <-chan cache.Delta[string, *state.Intercept] {
	filtered := make(chan cache.Delta[string, *state.Intercept], 1)
	go func() {
		defer close(filtered)
		included := make(map[string]*state.Intercept)
		initialized := false
		for {
			select {
			case <-ctx.Done():
				return
			case delta, ok := <-source:
				if !ok {
					return
				}
				next := cache.Delta[string, *state.Intercept]{}
				for id := range delta.Removals {
					if previous, exists := included[id]; exists {
						if next.Removals == nil {
							next.Removals = make(map[string]*state.Intercept)
						}
						next.Removals[id] = previous
						delete(included, id)
					}
				}
				for id, intercept := range delta.Upserts {
					if include == nil || include(id, intercept) {
						if next.Upserts == nil {
							next.Upserts = make(map[string]*state.Intercept)
						}
						next.Upserts[id] = intercept
						included[id] = intercept
						continue
					}
					if previous, exists := included[id]; exists {
						if next.Removals == nil {
							next.Removals = make(map[string]*state.Intercept)
						}
						next.Removals[id] = previous
						delete(included, id)
					}
				}
				if initialized && len(next.Upserts) == 0 && len(next.Removals) == 0 {
					continue
				}
				initialized = true
				select {
				case <-ctx.Done():
					return
				case filtered <- next:
				}
			}
		}
	}()
	return filtered
}

func (s *service) watchIntercepts(ctx context.Context, session *rpc.SessionInfo) (<-chan cache.Delta[string, *state.Intercept], <-chan struct{}, error) {
	sessionID := tunnel.SessionID(session.GetSessionId())
	var sessionDone <-chan struct{}
	var filter func(id string, info *state.Intercept) bool
	participantAwareFilter := false
	if sessionID == "" {
		filter = func(id string, info *state.Intercept) bool {
			return info.Disposition != rpc.InterceptDispositionType_REMOVED && !state.IsChildIntercept(info.Spec)
		}
	} else {
		var err error
		if sessionDone, err = s.state.SessionDone(sessionID); err != nil {
			return nil, nil, err
		}

		if agent := s.state.GetAgent(sessionID); agent != nil {
			if err := agentOwnershipError(ctx, sessionID, agent); err != nil {
				return nil, nil, err
			}
			filter = func(id string, info *state.Intercept) bool {
				if !state.AgentMatchesInterceptInfo(agent.AgentInfo, info) {
					// Don't return intercepts for different agents.
					return false
				}
				if as := s.state.GetAgent(sessionID); as == nil {
					return false
				}
				// Don't return intercepts that aren't in a "agent-owned" state.
				switch info.Disposition {
				case rpc.InterceptDispositionType_WAITING,
					rpc.InterceptDispositionType_ACTIVE,
					rpc.InterceptDispositionType_AGENT_ERROR:
					// agent-owned state: include the intercept
					return true
				case rpc.InterceptDispositionType_REMOVED:
					return true
				default:
					// otherwise: don't return this intercept
					clog.Debugf(ctx, "Intercept %q is in state %s", info.Spec.Name, info.Disposition)
					return false
				}
			}
			participantAwareFilter = true
		} else {
			// sessionID refers to a client session.
			client := s.state.GetClient(sessionID)
			if client == nil {
				return nil, nil, errors.Errorf(codes.NotFound, "Client session %q not found", sessionID)
			}
			if err := state.ClientOwnershipError(ctx, sessionID, client); err != nil {
				return nil, nil, err
			}
			filter = func(id string, info *state.Intercept) bool {
				return info.ClientSession.SessionId == string(sessionID) &&
					info.Disposition != rpc.InterceptDispositionType_REMOVED &&
					!state.IsChildIntercept(info.Spec)
			}
		}
	}

	if participantAwareFilter {
		return filterInterceptDeltas(ctx, s.state.WatchIntercepts(ctx, nil), filter), sessionDone, nil
	}
	return s.state.WatchIntercepts(ctx, filter), sessionDone, nil
}

// WatchIntercepts notifies a client or agent of the set of intercepts
// relevant to that client or agent.
func (s *service) WatchIntercepts(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.InterceptInfoSnapshot]) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	deltaCh, sessionDone, err := s.watchIntercepts(ctx, session)
	if err != nil {
		return err
	}
	snapshot := cache.NewClientMap[string, *state.Intercept]()
	return snapshot.Watch(sessionDone, deltaCh, func() error {
		clog.Debug(ctx, "Sending update")
		intercepts := make([]*rpc.InterceptInfo, 0, snapshot.Size())
		snapshot.Range(func(_ string, intercept *state.Intercept) bool {
			intercepts = append(intercepts, intercept.InterceptInfo)
			return true
		})
		sort.Slice(intercepts, func(i, j int) bool {
			return intercepts[i].Id < intercepts[j].Id
		})
		return stream.Send(&rpc.InterceptInfoSnapshot{
			Intercepts: intercepts,
		})
	})
}

func (s *service) WatchInterceptsDelta(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.InterceptInfoDelta]) error {
	ctx := stream.Context()
	if err := checkCompat(ctx, "WatchInterceptsDelta", "2.26.0"); err != nil {
		return err
	}
	ctx = managerutil.WithSessionInfo(ctx, session)
	deltaCh, sessionDone, err := s.watchIntercepts(ctx, session)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sessionDone:
			return nil
		case delta := <-deltaCh:
			iid := rpc.InterceptInfoDelta{Removals: maps2.KeySlice(delta.Removals)}
			if rl := len(delta.Upserts); rl > 0 {
				iid.Upserts = make(map[string]*rpc.InterceptInfo, rl)
				for k, v := range delta.Upserts {
					iid.Upserts[k] = v.InterceptInfo
				}
			}
			clog.Debugf(ctx, "Sending %d upserts and %d removals", len(iid.Upserts), len(iid.Removals))
			err = stream.Send(&iid)
			if err != nil {
				return err
			}
		}
	}
}

// WatchSessionEvents multiplexes the agent-pod projection (scoped to the
// connected namespace plus the requested namespaces, same shape and
// semantics as watchAgentPodsDelta) and this client's own intercepts onto a
// single stream.
func (s *service) WatchSessionEvents(request *rpc.SessionEventsRequest, stream grpc.ServerStreamingServer[rpc.SessionEventsDelta]) error {
	ctx := stream.Context()
	if err := checkCompat(ctx, "WatchSessionEvents", "2.31.0"); err != nil {
		return err
	}
	ctx, clientInfo, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return err
	}
	namespaces, err := s.agentPodNamespaces(ctx, clientInfo, request.Namespaces)
	if err != nil {
		return err
	}
	if !slices.Contains(namespaces, clientInfo.Namespace) {
		namespaces = append(namespaces, clientInfo.Namespace)
		sort.Strings(namespaces)
	}

	clientSessionID := managerutil.GetSessionID(ctx)
	agentsCh, refreshCh, sessionDone := s.createAgentPodWatchers(ctx, namespaces)
	proj := s.newAgentPodProjection(ctx, clientSessionID)
	proj.run(ctx, sessionDone, agentsCh)
	agentPodInfosCh := proj.subscribe(ctx.Done())

	interceptsCh, _, err := s.watchIntercepts(ctx, request.Session)
	if err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-sessionDone:
			return nil
		case <-refreshCh:
			proj.refreshIntercepted()
		case delta := <-agentPodInfosCh:
			clog.Debugf(ctx, "Sending %d agent-pod upserts and %d agent-pod removals", len(delta.Upserts), len(delta.Removals))
			apid := &rpc.AgentPodInfoDelta{Upserts: delta.Upserts, Removals: maps2.KeySlice(delta.Removals)}
			if err := stream.Send(&rpc.SessionEventsDelta{AgentPods: apid}); err != nil {
				return err
			}
		case delta := <-interceptsCh:
			iid := rpc.InterceptInfoDelta{Removals: maps2.KeySlice(delta.Removals)}
			if rl := len(delta.Upserts); rl > 0 {
				iid.Upserts = make(map[string]*rpc.InterceptInfo, rl)
				for k, v := range delta.Upserts {
					iid.Upserts[k] = v.InterceptInfo
				}
			}
			clog.Debugf(ctx, "Sending %d intercept upserts and %d intercept removals", len(iid.Upserts), len(iid.Removals))
			if err := stream.Send(&rpc.SessionEventsDelta{Intercepts: &iid}); err != nil {
				return err
			}
		}
	}
}

func (s *service) PrepareIntercept(ctx context.Context, request *rpc.CreateInterceptRequest) (*rpc.PreparedIntercept, error) {
	clog.Debugf(ctx, "Intercept name %s", request.InterceptSpec.Name)
	ctx, client, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return nil, err
	}
	namespace, err := s.managedTargetNamespace(ctx, client, request.InterceptSpec.Namespace)
	if err != nil {
		return nil, err
	}
	request.InterceptSpec.Namespace = namespace
	return s.state.PrepareIntercept(ctx, request, client)
}

func (s *service) GetKnownWorkloadKinds(ctx context.Context, request *rpc.SessionInfo) (*rpc.KnownWorkloadKinds, error) {
	if err := checkCompat(ctx, "GetKnownWorkloadKinds", "2.20.0"); err != nil {
		return nil, err
	}
	ctx = managerutil.WithSessionInfo(ctx, request)
	enabledWorkloadKinds := managerutil.GetEnv(ctx).EnabledWorkloadKinds
	kinds := make([]rpc.WorkloadInfo_Kind, len(enabledWorkloadKinds))
	for i, wlKind := range enabledWorkloadKinds {
		kinds[i] = workload.RpcKind(wlKind)
	}
	return &rpc.KnownWorkloadKinds{Kinds: kinds}, nil
}

func (s *service) EnsureAgent(ctx context.Context, request *rpc.EnsureAgentRequest) (*rpc.AgentInfoSnapshot, error) {
	ctx, client, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return nil, err
	}
	ns, err := s.managedTargetNamespace(ctx, client, request.Namespace)
	if err != nil {
		return nil, err
	}
	as, err := s.state.EnsureAgent(ctx, managerutil.GetSessionID(ctx), request.Name, ns, request.NodeAgent)
	if err != nil {
		return nil, status.Convert(err).Err()
	}
	if len(as) == 0 {
		return nil, errors.Errorf(codes.Internal, "failed to ensure agent for workload %s: no agents became active", request.Name)
	}
	rpcAs := make([]*rpc.AgentInfo, len(as))
	for i, a := range as {
		rpcAs[i] = a.AgentInfo
	}
	lastActivity := time.Now()
	if client.Mark(lastActivity) {
		clog.Tracef(ctx, "Last activity %s", lastActivity)
	}
	return &rpc.AgentInfoSnapshot{Agents: rpcAs}, nil
}

// ReleaseAgent releases the caller's claim on a node-agent previously
// ensured with node_agent=true in EnsureAgent. It is a no-op for a sidecar
// agent or an unknown claim.
func (s *service) ReleaseAgent(ctx context.Context, request *rpc.ReleaseAgentRequest) (*empty.Empty, error) {
	ctx, client, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return nil, err
	}
	ns, err := s.managedTargetNamespace(ctx, client, request.Namespace)
	if err != nil {
		return nil, err
	}
	if err := s.state.ReleaseAgent(ctx, managerutil.GetSessionID(ctx), request.Name, ns); err != nil {
		return nil, status.Convert(err).Err()
	}
	return &empty.Empty{}, nil
}

// CreateIntercept lets a client create an intercept.
func (s *service) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	ctx = managerutil.WithSessionInfo(ctx, ciReq.GetSession())
	spec := ciReq.InterceptSpec
	clog.Debugf(ctx, "Intercept name %s", ciReq.InterceptSpec.Name)

	ctx, client, err := s.ensureClientSession(ctx, ciReq.GetSession())
	if err != nil {
		return nil, err
	}
	namespace, err := s.managedTargetNamespace(ctx, client, spec.Namespace)
	if err != nil {
		return nil, err
	}
	spec.Namespace = namespace

	if val := validateIntercept(spec); val != "" {
		return nil, status.Error(codes.InvalidArgument, val)
	}

	if err := s.authorizeIntercept(ctx, namespace, spec); err != nil {
		if s.authMode == auth.ModeEnforcing {
			return nil, err
		}
		clog.Warnf(ctx, "intercept %q in namespace %s: %v (not enforced)", spec.Name, namespace, err)
	}

	client, interceptInfo, err := s.state.AddIntercept(ctx, ciReq)
	if err != nil {
		return nil, err
	}

	SetGauge(ctx, s.state.GetInterceptActiveStatus(), client.Name, client.InstallId, &spec.Name, 1)

	IncrementInterceptCounterFunc(ctx, s.state.GetInterceptCounter(), client.Name, client.InstallId, spec)
	lastActivity := time.Now()
	if client.Mark(lastActivity) {
		clog.Tracef(ctx, "Last activity %s", lastActivity)
	}

	return interceptInfo, nil
}

// authorizeIntercept returns nil when the caller may intercept in namespace, a
// PermissionDenied error when its RBAC disallows it, and an Unavailable error
// when authorization could not be determined. An unauthenticated caller is
// skipped -- there is no identity to review -- unless the manager is in
// ModeEnforcing, where a nil principal can only mean the interceptor let a
// tokenless call through for an exempt method; treat it as Unauthenticated
// rather than silently skipping the review.
func (s *service) authorizeIntercept(ctx context.Context, namespace string, spec *rpc.InterceptSpec) error {
	p := auth.PrincipalFrom(ctx)
	if p == nil {
		if s.authMode == auth.ModeEnforcing {
			return errors.Errorf(codes.Unauthenticated, "intercept creation requires an authenticated caller")
		}
		clog.Debugf(ctx, "caller is unauthenticated; skipping intercept authorization")
		return nil
	}
	podNames, err := workloadPodNames(ctx, spec.Agent, namespace)
	if err != nil {
		clog.Debugf(ctx, "unable to list pods for %s.%s; checking namespace-wide access only: %v", spec.Agent, namespace, err)
	}
	allowed, err := s.authorizer.CanPortForward(ctx, p, namespace, podNames)
	if err != nil {
		return errors.Errorf(codes.Unavailable, "unable to determine whether %s may create pods/portforward in namespace %s: %v", p.Username, namespace, err)
	}
	if !allowed {
		return errors.Errorf(codes.PermissionDenied, "%s is not permitted to create pods/portforward in namespace %s", p.Username, namespace)
	}
	return nil
}

// workloadPodNames returns the names of the current pods of the workload
// called name in namespace.
func workloadPodNames(ctx context.Context, name, namespace string) ([]string, error) {
	wl, err := k8sapi.GetWorkload(ctx, name, namespace, "")
	if err != nil {
		return nil, err
	}
	selector, err := wl.Selector()
	if err != nil {
		return nil, err
	}
	pods, err := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, len(pods.Items))
	for i := range pods.Items {
		names[i] = pods.Items[i].Name
	}
	return names, nil
}

func (s *service) MakeInterceptID(ctx context.Context, sessionID string, name string) (string, error) {
	// When something without a session ID (e.g. System A) calls this function,
	// it is sending the intercept ID as the name, so we use that.
	//
	// TODO: Look at cmd/traffic/cmd/manager/internal/state API and see if it makes
	// sense to make more / all functions use intercept ID instead of session ID + name.
	// Or at least functions outside services (e.g. SystemA), which don't know about sessions,
	// use in requests.
	if sessionID == "" {
		return name, nil
	}
	sid := tunnel.SessionID(sessionID)
	client := s.state.GetClient(sid)
	if client == nil {
		return "", errors.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}
	if err := state.ClientOwnershipError(ctx, sid, client); err != nil {
		return "", err
	}
	return sessionID + ":" + name, nil
}

// RemoveIntercept lets a client remove an intercept.
func (s *service) RemoveIntercept(ctx context.Context, riReq *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	name := riReq.Name
	clog.Debugf(ctx, "Intercept name %s", name)

	ctx, client, err := s.ensureClientSession(ctx, riReq.Session)
	if err != nil {
		return nil, err
	}
	SetGauge(ctx, s.state.GetInterceptActiveStatus(), client.Name, client.InstallId, &name, 0)

	s.state.RemoveIntercept(string(managerutil.GetSessionID(ctx)) + ":" + name)
	return &empty.Empty{}, nil
}

// GetIntercept gets an intercept info from intercept name.
func (s *service) GetIntercept(ctx context.Context, request *rpc.GetInterceptRequest) (*rpc.InterceptInfo, error) {
	interceptID, err := s.MakeInterceptID(ctx, request.GetSession().GetSessionId(), request.GetName())
	if err != nil {
		return nil, err
	}
	if intercept, ok := s.state.GetIntercept(interceptID); ok {
		return intercept.InterceptInfo, nil
	}
	return nil, status.Errorf(codes.NotFound, "Intercept named %q not found", request.Name)
}

// ReviewIntercept lets an agent approve or reject an intercept.
func (s *service) ReviewIntercept(ctx context.Context, rIReq *rpc.ReviewInterceptRequest) (*empty.Empty, error) {
	ceptID := rIReq.Id

	ctx, agent, err := s.ensureAgentSession(ctx, rIReq.GetSession())
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return &empty.Empty{}, nil
		}
		return nil, err
	}

	if rIReq.Disposition == rpc.InterceptDispositionType_AGENT_ERROR {
		clog.Errorf(ctx, "%s - %s: %s", ceptID, rIReq.Disposition, rIReq.Message)
	} else {
		clog.Debugf(ctx, "%s - %s", ceptID, rIReq.Disposition)
	}

	s.removeExcludedEnvVars(rIReq.Environment)

	intercept := s.state.ApplyAgentReview(ctx, ceptID, agent, rIReq)

	if intercept == nil {
		return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", ceptID)
	}

	return &empty.Empty{}, nil
}

func (s *service) removeExcludedEnvVars(envVars map[string]string) {
	for _, key := range s.configWatcher.GetAgentEnv().Excluded {
		delete(envVars, key)
	}
}

func (s *service) Tunnel(server grpc.BidiStreamingServer[rpc.TunnelMessage, rpc.TunnelMessage]) error {
	ctx := server.Context()
	stream, err := tunnel.NewServerStream(ctx, tunnel.ClientToManager, server)
	if err != nil {
		return errors.FromError(err, codes.FailedPrecondition, fmt.Sprintf("failed to connect stream: %v", err))
	}
	return s.state.Tunnel(ctx, stream)
}

// GetQuicTunnelEndpoint returns the descriptor for the traffic-manager's QUIC endpoint.
// The endpoint is only advertised once the listener is enabled and at least one
// candidate address exists for it -- explicit (TunnelQuicExternalHost) or discovered
// (see "Zero-configuration endpoint discovery" in docs/reference/quic-transport-architecture.md);
// otherwise the client is told to keep using the port-forwarded gRPC transport.
func (s *service) GetQuicTunnelEndpoint(ctx context.Context, session *rpc.SessionInfo) (*rpc.QuicTunnelEndpoint, error) {
	if err := checkCompat(ctx, "GetQuicTunnelEndpoint", "2.31.0"); err != nil {
		return nil, err
	}
	env := managerutil.GetEnv(ctx)
	if env.TunnelQuicPort == 0 {
		// The QUIC CA now always exists (see NewService), but the tunnel listener
		// itself is still gated on this port; behavior here is unchanged.
		return &rpc.QuicTunnelEndpoint{Enabled: false}, nil
	}
	candidates := s.quicCandidates(env)
	if len(candidates) == 0 {
		return &rpc.QuicTunnelEndpoint{Enabled: false}, nil
	}
	sessionID := tunnel.SessionID(session.GetSessionId())
	client := s.state.GetClient(sessionID)
	if client == nil {
		return nil, errors.Errorf(codes.NotFound, "Session %q not found", sessionID)
	}
	if err := state.ClientOwnershipError(ctx, sessionID, client); err != nil {
		return nil, err
	}
	certPEM, keyPEM, err := s.quicCA.MintClientCert(string(sessionID))
	if err != nil {
		return nil, errors.FromError(err, codes.Internal, fmt.Sprintf("failed to mint QUIC client certificate: %v", err))
	}
	// host/port duplicate the first candidate so a client built before the candidates
	// field existed still gets a single address to dial; see the proto comment on
	// QuicTunnelEndpoint.candidates.
	first := candidates[0]
	return &rpc.QuicTunnelEndpoint{
		Enabled:       true,
		Host:          first.Host,
		Port:          first.Port,
		CaPem:         s.quicCA.CertPEM(),
		ClientCertPem: certPEM,
		ClientKeyPem:  keyPEM,
		ServerName:    quictunnel.ServerName,
		Alpn:          tunnel.QuicALPN,
		Candidates:    candidates,
	}, nil
}

// quicCandidates returns the ordered candidate list to advertise: exactly the explicit
// TunnelQuicExternalHost override when one is configured (bypassing discovery
// entirely, per the design's override semantics), otherwise whatever quicDiscovery has
// found so far. Returns nil (not enabled) when neither applies.
func (s *service) quicCandidates(env *managerutil.Env) []*rpc.QuicEndpointCandidate {
	if env.TunnelQuicExternalHost != "" {
		port := env.TunnelQuicExternalPort
		if port == 0 {
			port = env.TunnelQuicPort
		}
		return []*rpc.QuicEndpointCandidate{{Host: env.TunnelQuicExternalHost, Port: int32(port)}}
	}
	if s.quicDiscovery == nil {
		return nil
	}
	found := s.quicDiscovery.Candidates()
	if len(found) == 0 {
		return nil
	}
	candidates := make([]*rpc.QuicEndpointCandidate, len(found))
	for i, c := range found {
		candidates[i] = &rpc.QuicEndpointCandidate{Host: c.Host, Port: c.Port}
	}
	return candidates
}

// GetQuicAgentCert mints a QUIC server certificate for the calling agent's own SNI
// name, so it can run a QUIC listener behind the forwarder. See "Agent connections
// over QUIC" in docs/reference/quic-transport-architecture.md.
//
// Unlike GetQuicTunnelEndpoint, the caller's session must already be an agent
// session (established via ArriveAsAgent/ReconnectAgent): a client has no pod UID
// and thus no SNI name to mint for, and GetQuicAgentCert never validates a client
// session's SessionInfo, whether or not the QUIC CA is enabled.
func (s *service) GetQuicAgentCert(ctx context.Context, session *rpc.SessionInfo) (*rpc.QuicAgentCert, error) {
	if err := checkCompat(ctx, "GetQuicAgentCert", "2.31.0"); err != nil {
		return nil, err
	}
	_, agent, err := s.ensureAgentSession(ctx, session)
	if err != nil {
		return nil, err
	}
	if s.quicCA == nil {
		// Defensive only: NewService always creates the CA now.
		return &rpc.QuicAgentCert{Enabled: false}, nil
	}
	sni := quicfwd.AgentSNI(agent.PodUid)
	cert, err := s.quicCA.MintServerCert(sni)
	if err != nil {
		return nil, errors.FromError(err, codes.Internal, fmt.Sprintf("failed to mint QUIC agent certificate: %v", err))
	}
	certPEM, keyPEM, err := quictunnel.ServerCertToPEM(cert)
	if err != nil {
		return nil, errors.FromError(err, codes.Internal, fmt.Sprintf("failed to encode QUIC agent certificate: %v", err))
	}
	return &rpc.QuicAgentCert{
		Enabled:            true,
		CertPem:            certPEM,
		KeyPem:             keyPEM,
		CaPem:              s.quicCA.CertPEM(),
		Sni:                sni,
		AuthenticationMode: s.authMode.String(),
	}, nil
}

// GetSessionCredential returns the session-scoped credential -- a client certificate
// and a signed bearer token, both naming the caller's session -- used to authenticate
// against a traffic-agent's file-sharing and gRPC ports. See "Traffic-agent ports" in
// docs/reference/authentication.md. Unlike GetQuicTunnelEndpoint, this works regardless
// of whether the QUIC tunnel listener is enabled: the QUIC CA now always exists (see
// NewService), and the credential this mints is used by transports that have nothing to
// do with the QUIC tunnel.
func (s *service) GetSessionCredential(ctx context.Context, session *rpc.SessionInfo) (*rpc.SessionCredential, error) {
	if err := checkCompat(ctx, "GetSessionCredential", "2.31.2"); err != nil {
		return nil, err
	}
	sessionID := tunnel.SessionID(session.GetSessionId())
	client := s.state.GetClient(sessionID)
	if client == nil {
		return nil, errors.Errorf(codes.NotFound, "Session %q not found", sessionID)
	}
	if err := state.ClientOwnershipError(ctx, sessionID, client); err != nil {
		return nil, err
	}
	certPEM, keyPEM, err := s.quicCA.MintClientCert(string(sessionID))
	if err != nil {
		return nil, errors.FromError(err, codes.Internal, fmt.Sprintf("failed to mint session client certificate: %v", err))
	}
	token, expiry, err := s.quicCA.MintSessionToken(string(sessionID))
	if err != nil {
		return nil, errors.FromError(err, codes.Internal, fmt.Sprintf("failed to mint session token: %v", err))
	}
	return &rpc.SessionCredential{
		CaPem:         s.quicCA.CertPEM(),
		ClientCertPem: certPEM,
		ClientKeyPem:  keyPEM,
		Token:         token,
		Expiry:        timestamppb.New(expiry),
	}, nil
}

// quicSNIForAgent returns the SNI name a client dials, through the QUIC forwarder,
// to reach an agent's QUIC listener -- or "" when quicPort is not positive, meaning
// the agent has no QUIC listener and must be reached by port-forward only.
func quicSNIForAgent(quicPort int32, podUID string) string {
	if quicPort <= 0 {
		return ""
	}
	return quicfwd.AgentSNI(podUID)
}

// quicBackendWatchDebounce coalesces a burst of agent-session changes (e.g. a
// rollout replacing every agent pod at once) into a single QuicBackendSnapshot,
// mirroring the debounce pattern used by the node-agent pod-set watcher
// (state.nodeAgentPodWatchDebounce).
const quicBackendWatchDebounce = 250 * time.Millisecond

// quicManagerBackends returns this traffic-manager pod's own QuicBackend entries,
// derived from the POD_IP the chart's downward API sets on the container. It is
// empty when that env var is unset, which is normal outside of a real cluster
// (e.g. unit tests that don't populate managerutil.Env.PodIp).
func (s *service) quicManagerBackends(ctx context.Context) []*rpc.QuicBackend {
	env := managerutil.GetEnv(ctx)
	podIP := env.PodIp
	if !podIP.IsValid() {
		return nil
	}
	return []*rpc.QuicBackend{{Ip: podIP.AsSlice(), Kind: "manager", Port: int32(env.TunnelQuicPort)}}
}

// WatchQuicBackends notifies the QUIC forwarder (a separate, stateless packet
// router; see docs/reference/quic-transport-architecture.md, "The forwarder") of the set
// of pod IPs it may route QUIC traffic to. Unlike the other Watch* RPCs this
// call carries no SessionInfo and is callable without an established session,
// the same way Version and GetTelepresenceAPI are: the forwarder has no client
// session of its own.
//
// The first QuicBackendSnapshot -- this manager's own pod IP plus the pod IP
// of every currently live agent session -- is sent immediately. A new,
// full-replacement snapshot follows whenever the agent set changes, coalesced
// by quicBackendWatchDebounce so a burst of churn (a rollout, a mass
// reconnect) produces one snapshot instead of one per event.
func (s *service) WatchQuicBackends(_ *empty.Empty, stream grpc.ServerStreamingServer[rpc.QuicBackendSnapshot]) error {
	ctx := stream.Context()
	if err := checkCompat(ctx, "WatchQuicBackends", "2.31.0"); err != nil {
		return err
	}
	managerBackends := s.quicManagerBackends(ctx)
	agentsCh := s.state.WatchAgents(ctx, nil)
	m := mutator.GetMap(ctx)
	agents := make(map[tunnel.SessionID]*state.AgentSession)

	applyDelta := func(delta cache.Delta[tunnel.SessionID, *state.AgentSession]) (changed bool) {
		for id, a := range delta.Upserts {
			agents[id] = a
			changed = true
		}
		for id := range delta.Removals {
			delete(agents, id)
			changed = true
		}
		return changed
	}

	buildSnapshot := func() *rpc.QuicBackendSnapshot {
		backends := slices.Clone(managerBackends)
		for _, a := range agents {
			// Only agents with a QUIC listener of their own are useful
			// forwarder backends; an agent that never fetched or never got a
			// QUIC port has nothing behind it to route to.
			if a.QuicPort <= 0 {
				continue
			}
			if m.IsInactive(types.UID(a.PodUid)) {
				continue
			}
			aip, err := netip.ParseAddr(a.PodIp)
			if err != nil {
				clog.Errorf(ctx, "quic backend allowlist: error parsing agent pod ip %q: %v", a.PodIp, err)
				continue
			}
			backends = append(backends, &rpc.QuicBackend{
				Ip:     aip.AsSlice(),
				Kind:   "agent",
				Port:   a.QuicPort,
				PodUid: a.PodUid,
			})
		}
		return &rpc.QuicBackendSnapshot{Backends: backends}
	}

	// The first delta on agentsCh is always the current full snapshot
	// (cache.Map.Subscribe semantics); send it right away, with no debounce.
	select {
	case <-ctx.Done():
		return nil
	case delta, ok := <-agentsCh:
		if !ok {
			return nil
		}
		applyDelta(delta)
	}
	if err := stream.Send(buildSnapshot()); err != nil {
		return err
	}

	debounce := time.NewTimer(quicBackendWatchDebounce)
	if !debounce.Stop() {
		<-debounce.C
	}
	pending := false
	for {
		var debounceCh <-chan time.Time
		if pending {
			debounceCh = debounce.C
		}
		select {
		case <-ctx.Done():
			return nil
		case delta, ok := <-agentsCh:
			if !ok {
				return nil
			}
			if applyDelta(delta) && !pending {
				pending = true
				debounce.Reset(quicBackendWatchDebounce)
			}
		case <-debounceCh:
			pending = false
			if err := stream.Send(buildSnapshot()); err != nil {
				return err
			}
		}
	}
}

// hasDomainSuffix checks if the given name is suffixed with the given suffix. The following
// rules apply:
//
//   - The name must end with a dot.
//   - The suffix may optionally end with a dot.
//   - The suffix may not be empty.
//   - The suffix match must follow after a dot in the name, or match the whole name.
func hasDomainSuffix(name, suffix string) bool {
	sl := len(suffix)
	if sl == 0 {
		return false
	}
	nl := len(name)
	sfp := nl - sl
	if sfp < 0 {
		return false
	}
	if name[nl-1] != '.' {
		return false
	}
	if suffix[sl-1] != '.' {
		if sfp == 0 {
			return false
		}
		sfp--
		name = name[0 : nl-1]
	}
	if sfp == 0 {
		return name == suffix
	}
	return name[sfp-1] == '.' && name[sfp:] == suffix
}

func (s *service) resolveSelfDNS(svcIP net.IP, request *rpc.DNSRequest) (rrs dnsproxy.RRs) {
	qType := uint16(request.Type)
	switch qType {
	case dns2.TypeA:
		rrs = dnsproxy.RRs{&dns2.A{
			Hdr: dns2.RR_Header{
				Name:   request.Name,
				Rrtype: qType,
				Class:  dns2.ClassINET,
			},
			A: svcIP.To4(),
		}}
	case dns2.TypeAAAA:
		var ip net.IP
		if svcIP.To4() == nil {
			ip = svcIP.To16()
		}
		rrs = dnsproxy.RRs{&dns2.AAAA{
			Hdr: dns2.RR_Header{
				Name:   request.Name,
				Rrtype: qType,
				Class:  dns2.ClassINET,
			},
			AAAA: ip,
		}}
	case dns2.TypeCNAME:
		rrs = dnsproxy.RRs{&dns2.CNAME{
			Hdr: dns2.RR_Header{
				Name:   request.Name,
				Rrtype: qType,
				Class:  dns2.ClassINET,
			},
			Target: s.serviceNameFQN,
		}}
	}
	return rrs
}

func (s *service) Lookup(ctx context.Context, request *rpc.LookupRequest) (response *rpc.LookupResponse, err error) {
	clog.Debugf(ctx, "lookup %q", request.Name)
	var ips []netip.Addr
	defer func() {
		if err == nil {
			clog.Debugf(ctx, "lookup %q => %v", request.Name, ips)
		}
	}()
	ctx, client, err := s.ensureClientSession(ctx, request.Session)
	if err != nil {
		return nil, err
	}

	if svcIP := s.clusterInfo.ServiceIP(); svcIP != nil && (request.Name == s.serviceNameNs || request.Name == s.serviceNameFQN) {
		addr, _ := netip.AddrFromSlice(svcIP)
		b, _ := addr.MarshalBinary()
		return &rpc.LookupResponse{Ips: [][]byte{b}}, nil
	}
	tmNamespace := managerutil.GetEnv(ctx).ManagerNamespace
	name := request.Name
	// Name must be at least one character long and end with a dot.
	nl := len(name)
	if nl < 2 || name[nl-1] != '.' {
		return nil, status.Errorf(codes.InvalidArgument, "empty name")
	}
	nDots := 0
	for _, c := range name {
		if c == '.' {
			nDots++
		}
	}
	if nDots == 1 && client.Namespace != tmNamespace {
		name += client.Namespace
	} else {
		// Strip trailing dot in query.
		name = name[:nl-1]
	}
	clog.Debugf(ctx, `LookupNetIP("ip", %q)`, name)
	ips, err = net.DefaultResolver.LookupNetIP(ctx, "ip", name)
	if err != nil {
		_, err = dnsproxy.MakeDNSError(err)
		if err != nil {
			return nil, err
		}
	}
	response = &rpc.LookupResponse{}
	if len(ips) > 0 {
		response.Ips = make([][]byte, len(ips))
		for i, ip := range ips {
			if ip.Is4In6() {
				ip = netip.AddrFrom4(ip.As4())
				ips[i] = ip
			}
			response.Ips[i], _ = ip.MarshalBinary()
		}
	}
	return response, nil
}

func (s *service) LookupDNS(ctx context.Context, request *rpc.DNSRequest) (response *rpc.DNSResponse, err error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSession())
	qType := uint16(request.Type)
	qtn := dns2.TypeToString[qType]
	var rrs dnsproxy.RRs
	if clog.Enabled(ctx, slog.LevelDebug) {
		defer func() {
			var result string
			switch {
			case err != nil:
				result = err.Error()
			case len(rrs) == 0:
				result = dns2.RcodeToString[int(response.RCode)]
			default:
				result = rrs.String()
			}
			clog.Debugf(ctx, "%s %s -> %s", request.Name, qtn, result)
		}()
	}

	if svcIP := s.clusterInfo.ServiceIP(); svcIP != nil && (request.Name == s.serviceNameNs || request.Name == s.serviceNameFQN) {
		rrs = s.resolveSelfDNS(svcIP, request)
		if len(rrs) > 0 {
			return dnsproxy.ToRPC(rrs, dns2.RcodeSuccess)
		}
	}

	sessionID := tunnel.SessionID(request.GetSession().GetSessionId())
	if client := s.state.GetClient(sessionID); client != nil {
		if err := state.ClientOwnershipError(ctx, sessionID, client); err != nil {
			return nil, err
		}
	}
	noSearchDomain := s.dotClusterDomain
	rrs, rCode := s.lookupFromManager(ctx, sessionID, qType, request.Name, noSearchDomain)
	return dnsproxy.ToRPC(rrs, rCode)
}

func (s *service) lookupFromManager(ctx context.Context, sessionID tunnel.SessionID, qType uint16, qName, noSearchDomain string) (dnsproxy.RRs, int) {
	name := qName
	client := s.state.GetClient(sessionID)
	tmNamespace := managerutil.GetEnv(ctx).ManagerNamespace
	restoreName := false
	nDots := 0
	if client != nil {
		for _, c := range name {
			if c == '.' {
				nDots++
			}
		}
		if nDots == 1 && client.Namespace != tmNamespace {
			name += client.Namespace + "."
			restoreName = true
		}
	}
	clog.Tracef(ctx, "traffic-manager: %s", name)
	qtn := dns2.TypeToString[qType]
	rrs, rCode, err := dnsproxy.Lookup(ctx, qType, name, noSearchDomain)
	if err == nil && rCode == dns2.RcodeNameError {
		// Could still be x.y.<client namespace>, but let's avoid x.<cluster domain>.<client namespace> and x.<client-namespace>.<client namespace>
		if client != nil && nDots > 1 && client.Namespace != tmNamespace && !strings.HasSuffix(name, s.dotClusterDomain) && !hasDomainSuffix(name, client.Namespace) {
			name += client.Namespace + "."
			restoreName = true
			clog.Debugf(ctx, "traffic-manager: %s", name)
			rrs, rCode, err = dnsproxy.Lookup(ctx, qType, name, noSearchDomain)
		}
	}
	if err != nil {
		clog.Errorf(ctx, "traffic-manager: %s %s -> %s %s", qName, qtn, dns2.RcodeToString[rCode], err)
		return nil, rCode
	}
	if len(rrs) == 0 {
		clog.Tracef(ctx, "traffic-manager: %s %s -> %s", qName, qtn, dns2.RcodeToString[rCode])
	} else {
		if restoreName {
			clog.Tracef(ctx, "traffic-manager: restore %s to %s", name, qName)
			for _, rr := range rrs {
				rr.Header().Name = qName
			}
		}
		clog.Tracef(ctx, "traffic-manager: %s %s -> %s", qName, qtn, rrs)
	}
	return rrs, rCode
}

// GetLogs acquires the logs for the traffic-manager and/or traffic-agents specified by the
// GetLogsRequest and returns them to the caller
//
// Deprecated: Clients should use the user daemon's GatherLogs method.
func (s *service) GetLogs(_ context.Context, _ *rpc.GetLogsRequest) (*rpc.LogsResponse, error) {
	return &rpc.LogsResponse{
		PodLogs: make(map[string]string),
		PodYaml: make(map[string]string),
		ErrMsg:  "traffic-manager.GetLogs is deprecated. Please upgrade your telepresence client",
	}, nil
}

// SetLogLevel applies a temporary log-level change. A request that carries a
// session has its ownership verified; a nil principal on that session is
// rejected in ModeEnforcing, since it can only mean an exempt method let a
// tokenless call through. A request without a session is rejected outright in
// ModeEnforcing, and allowed, with a debug log, otherwise.
func (s *service) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (*empty.Empty, error) {
	if session := request.GetSession(); session != nil {
		var err error
		ctx, _, err = s.ensureClientSession(ctx, session)
		if err != nil {
			return nil, err
		}
		if auth.PrincipalFrom(ctx) == nil && s.authMode == auth.ModeEnforcing {
			return nil, errors.Errorf(codes.Unauthenticated, "setting the log level requires an authenticated caller")
		}
	} else if s.authMode == auth.ModeEnforcing {
		return nil, errors.Errorf(codes.Unauthenticated, "setting the log level requires a client session")
	} else {
		clog.Debugf(ctx, "unauthenticated log level request allowed; manager is not in enforcing mode")
	}

	err := s.state.SetTempLogLevel(ctx, request)
	if err != nil {
		err = errors.FromError(err, codes.InvalidArgument, err.Error())
	}
	return &empty.Empty{}, err
}

func (s *service) UninstallAgents(ctx context.Context, request *rpc.UninstallAgentsRequest) (*empty.Empty, error) {
	ctx = managerutil.WithSessionInfo(ctx, request.GetSessionInfo())
	clog.Debugf(ctx, "%s", request.Agents)
	return &empty.Empty{}, s.state.UninstallAgents(ctx, request)
}

func (s *service) WatchLogLevel(_ *empty.Empty, stream grpc.ServerStreamingServer[rpc.LogLevelRequest]) error {
	return s.state.WaitForTempLogLevel(stream)
}

func (s *service) WatchClusterInfo(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.ClusterInfo]) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	sessionDone, err := s.state.SessionDone(tunnel.SessionID(session.SessionId))
	if err != nil {
		return err
	}
	return s.clusterInfo.Watch(ctx, sessionDone, stream)
}

func (s *service) WatchWorkloads(request *rpc.WorkloadEventsRequest, stream grpc.ServerStreamingServer[rpc.WorkloadEventsDelta]) (err error) {
	ctx := stream.Context()
	// Dysfunctional prior to 2.21.0 because no initial snapshot was sent.
	if err := checkCompat(ctx, "WatchWorkloads", "2.21.0-alpha.4"); err != nil {
		return err
	}
	ctx = managerutil.WithSessionInfo(ctx, request.SessionInfo)
	clog.Debugf(ctx, "Namespace %q", request.Namespace)

	if request.SessionInfo == nil {
		return status.Error(codes.InvalidArgument, "SessionInfo is required")
	}
	clientSession := tunnel.SessionID(request.SessionInfo.SessionId)
	namespace := request.Namespace
	if namespace == "" {
		clientInfo := s.state.GetClient(clientSession)
		if clientInfo == nil {
			return status.Errorf(codes.NotFound, "Client session %q not found", clientSession)
		}
		if err := state.ClientOwnershipError(ctx, clientSession, clientInfo); err != nil {
			return err
		}
		namespace = clientInfo.Namespace
	} else if !s.State().ManagesNamespace(ctx, namespace) {
		return status.Error(codes.FailedPrecondition, fmt.Sprintf("namespace %s is not managed", namespace))
	}
	ww := s.state.NewWorkloadInfoWatcher(clientSession, namespace)
	return ww.Watch(ctx, stream)
}

func (s *service) updateTrafficManagerConfigMap(ctx context.Context) error {
	updatedAgentStateFileYAML := s.configWatcher.GetAgentStateYaml(ctx)
	patch := map[string]interface{}{
		"data": map[string]string{
			tmconfig.AgentStateFileName: string(updatedAgentStateFileYAML),
		},
	}

	namespace := managerutil.GetEnv(ctx).ManagerNamespace
	client := k8sapi.GetK8sInterface(ctx).CoreV1()
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = client.ConfigMaps(namespace).Patch(ctx, agentconfig.ManagerAppName, types.StrategicMergePatchType,
		patchBytes,
		metav1.PatchOptions{})

	if err == nil {
		s.tmConfigMapUpdated.Store(false)
	}
	return err
}

func (s *service) ensureClientSession(ctx context.Context, sessionInfo *rpc.SessionInfo) (context.Context, *state.ClientSession, error) {
	ctx = managerutil.WithSessionInfo(ctx, sessionInfo)
	sessionID := tunnel.SessionID(sessionInfo.SessionId)
	session := s.state.GetClient(sessionID)
	if session == nil {
		return ctx, nil, errors.Errorf(codes.NotFound, "Client session %q not found", sessionID)
	}
	if err := state.ClientOwnershipError(ctx, sessionID, session); err != nil {
		return ctx, nil, err
	}
	return ctx, session, nil
}

// ensureAgentSession looks up the agent session identified by sessionInfo and, when
// the session is bound to a verified pod identity, requires the caller's own
// principal to match that identity before granting access.
func (s *service) ensureAgentSession(ctx context.Context, sessionInfo *rpc.SessionInfo) (context.Context, *state.AgentSession, error) {
	ctx = managerutil.WithSessionInfo(ctx, sessionInfo)
	sessionID := tunnel.SessionID(sessionInfo.GetSessionId())
	session := s.state.GetAgent(sessionID)
	if session == nil {
		return ctx, nil, errors.Errorf(codes.NotFound, "Agent session %q not found", sessionID)
	}
	if err := agentOwnershipError(ctx, sessionID, session); err != nil {
		return ctx, nil, err
	}
	return ctx, session, nil
}

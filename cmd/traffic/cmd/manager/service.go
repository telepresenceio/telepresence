package manager

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/systema"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/cluster"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/state"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/connpool"
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
				var resp *systema.CreateDomainResponse
				resp, err = sa.CreateDomain(ctx, &systema.CreateDomainRequest{
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
					_, err := sa.RemoveDomain(ctx, &systema.RemoveDomainRequest{
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
				_, err := sa.RemoveDomain(ctx, &systema.RemoveDomainRequest{
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
		}
	})

	if intercept == nil {
		return nil, status.Errorf(codes.NotFound, "Intercept with ID %q not found for this session", ceptID)
	}

	return &empty.Empty{}, nil
}

func (m *Manager) ClientTunnel(server rpc.Manager_ClientTunnelServer) error {
	ctx := server.Context()
	muxTunnel := connpool.NewMuxTunnel(server)
	sessionInfo, err := readTunnelSessionID(ctx, muxTunnel)
	if err != nil {
		return err
	}
	_, err = muxTunnel.ReadPeerVersion(ctx)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to read client tunnel version: %v", err)
	}
	if err = muxTunnel.Send(ctx, connpool.VersionControl()); err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to send manager tunnel version: %v", err)
	}
	return m.state.ClientTunnel(managerutil.WithSessionInfo(ctx, sessionInfo), muxTunnel)
}

func (m *Manager) AgentTunnel(server rpc.Manager_AgentTunnelServer) error {
	ctx := server.Context()
	muxTunnel := connpool.NewMuxTunnel(server)
	agentSessionInfo, err := readTunnelSessionID(ctx, muxTunnel)
	if err != nil {
		return err
	}
	clientSessionInfo, err := readTunnelSessionID(ctx, muxTunnel)
	if err != nil {
		return err
	}
	_, err = muxTunnel.ReadPeerVersion(ctx)
	if err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to read agent tunnel version: %v", err)
	}
	if err = muxTunnel.Send(ctx, connpool.VersionControl()); err != nil {
		return status.Errorf(codes.FailedPrecondition, "failed to send manager tunnel version: %v", err)
	}
	return m.state.AgentTunnel(managerutil.WithSessionInfo(ctx, agentSessionInfo), clientSessionInfo, muxTunnel)
}

func readTunnelSessionID(ctx context.Context, server connpool.MuxTunnel) (*rpc.SessionInfo, error) {
	// Initial message must be the session info that this bidi stream should be attached to
	msg, err := server.Receive(ctx)
	if err != nil {
		return nil, status.Errorf(codes.FailedPrecondition, "failed to read session info message: %v", err)
	}
	var sessionInfo *rpc.SessionInfo
	if ctrl, ok := msg.(connpool.Control); ok {
		if sessionInfo = ctrl.SessionInfo(); sessionInfo != nil {
			return sessionInfo, nil
		}
	}
	return nil, status.Error(codes.FailedPrecondition, "first message was not session info")
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
func (m *Manager) GetLogs(ctx context.Context, request *rpc.GetLogsRequest) (*rpc.LogsResponse, error) {
	resp := &rpc.LogsResponse{
		PodLogs: make(map[string]string),
		PodYaml: make(map[string]string),
	}
	var errMsg string
	clientset := managerutil.GetK8sClientset(ctx)

	// getPodLogs is a helper function for getting the logs from the container
	// of a given pod. If we are unable to get a log for a given pod, we will
	// instead return the error in the map, instead of the log, so that:
	// - one failure doesn't prevent us from getting logs from other pods
	// - it is easy to figure out why gettings logs for a given pod failed
	getPodLogs := func(pods []*corev1.Pod, container string) {
		wg := sync.WaitGroup{}
		logWriteMutex := &sync.Mutex{}
		wg.Add(len(pods))
		for _, pod := range pods {
			go func(pod *corev1.Pod) {
				defer wg.Done()
				plo := &corev1.PodLogOptions{
					Container: container,
				}
				// Since the same named workload could exist in multiple namespaces
				// we add the namespace into the name so that it's easier to make
				// sense of the logs
				podAndNs := fmt.Sprintf("%s.%s", pod.Name, pod.Namespace)
				req := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, plo)
				podLogs, err := req.Stream(ctx)
				if err != nil {
					logWriteMutex.Lock()
					resp.PodLogs[podAndNs] = fmt.Sprintf("Failed to get logs: %s", err)
					logWriteMutex.Unlock()
					return
				}
				defer podLogs.Close()
				buf := new(bytes.Buffer)
				_, err = io.Copy(buf, podLogs)
				if err != nil {
					logWriteMutex.Lock()
					resp.PodLogs[podAndNs] = fmt.Sprintf("Failed writing log to buffer: %s", err)
					logWriteMutex.Unlock()
					return
				}
				logWriteMutex.Lock()
				resp.PodLogs[podAndNs] = buf.String()
				logWriteMutex.Unlock()

				// Get the pod yaml if the user asked for it
				if request.GetPodYaml {
					podYaml, err := yaml.Marshal(pod)
					if err != nil {
						logWriteMutex.Lock()
						resp.PodYaml[podAndNs] = fmt.Sprintf("Failed marshaling pod yaml: %s", err)
						logWriteMutex.Unlock()
						return
					}
					logWriteMutex.Lock()
					resp.PodYaml[podAndNs] = string(podYaml)
					logWriteMutex.Unlock()
				}
			}(pod)
		}
		wg.Wait()
	}
	// Get the pods that have traffic-agents
	agentPods, err := m.clusterInfo.GetTrafficAgentPods(ctx, request.Agents)
	if err != nil {
		errMsg += fmt.Sprintf("error getting traffic-agent pods: %s\n", err)
	} else {
		getPodLogs(agentPods, "traffic-agent")
	}

	// We want to get the traffic-manager logs *last* so that if we generate
	// any errors in the traffic-manager getting the traffic-agent pods, we
	// want those logs to appear in what we export
	if request.TrafficManager {
		managerPods, err := m.clusterInfo.GetTrafficManagerPods(ctx)
		if err != nil {
			errMsg += fmt.Sprintf("error getting traffic-manager pods: %s\n", err)
		} else {
			getPodLogs(managerPods, "traffic-manager")
		}
	}

	// If we were unable to get logs from the traffic-manager and/or traffic-agents
	// we put that information in the errMsg.
	if errMsg != "" {
		resp.ErrMsg = errMsg
	}
	return resp, nil
}

func (m *Manager) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (*empty.Empty, error) {
	m.state.SetTempLogLevel(ctx, request)
	return &empty.Empty{}, nil
}

func (m *Manager) WatchLogLevel(_ *empty.Empty, stream rpc.Manager_WatchLogLevelServer) error {
	ctx := stream.Context()
	dlog.Debugf(ctx, "WatchLogLevel called")

	ll := m.state.InitialTempLogLevel()
	dlog.Debugf(ctx, "InitialLogLevel %v", ll)
	for m.ctx.Err() == nil {
		if ll != nil {
			if err := stream.Send(ll); err != nil {
				dlog.Errorf(ctx, "WatchLogLevel.Send() failed: %v", err)
				break
			}
		}
		ll = m.state.WaitForTempLogLevel()
		dlog.Debugf(ctx, "AwaitedLogLevel %v", ll)
	}
	return nil
}

func (m *Manager) WatchClusterInfo(session *rpc.SessionInfo, stream rpc.Manager_WatchClusterInfoServer) error {
	ctx := managerutil.WithSessionInfo(stream.Context(), session)
	dlog.Debugf(ctx, "WatchClusterInfo called")
	return m.clusterInfo.Watch(ctx, stream)
}

// expire removes stale sessions.
func (m *Manager) expire(ctx context.Context) {
	m.state.ExpireSessions(ctx, m.clock.Now().Add(-15*time.Second))
}

func (m *Manager) HealthCheckBiStream(bistream rpc.Manager_HealthCheckBiStreamServer) error {
	ctx := bistream.Context()
	ch := m.state.HealthCheckPS.Subscribe()
	defer m.state.HealthCheckPS.Unsubscribe(ch)

	for {
		select {
		case <-ctx.Done():
			break
		case chGroup := <-ch:
			go func() {
				defer chGroup.Wg.Done()

				// request HealthCheck from agent
				err := bistream.Send(&empty.Empty{})
				if err != nil {
					dlog.Error(ctx, err)
					return
				}
				responce, err := bistream.Recv()
				if err != nil {
					dlog.Error(ctx, err)
					return
				}

				// report responce
				chGroup.Ch <- responce
			}()
		}
	}

	return nil
}

func (m *Manager) DoHealthCheck(ctx context.Context, _ *empty.Empty) (*rpc.HealthReport, error) {
	chGroup := m.state.HealthCheckPS.Publish()
	results := rpc.HealthReport{}
	for msg := range chGroup.Ch {
		results.HealthMessages = append(results.HealthMessages, msg)
	}
	return &results, nil
}

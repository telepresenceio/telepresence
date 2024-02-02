package agent

import (
	"context"
	"net/http"

	"github.com/blang/semver"
	"github.com/puzpuzpuz/xsync/v3"
	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/agent"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// State reflects the current state of the agent.
type State interface {
	Config
	agent.AgentServer
	tunnel.ClientStreamProvider
	AddInterceptState(is InterceptState)
	AgentState() restapi.AgentState
	InterceptStates() []InterceptState
	HandleIntercepts(ctx context.Context, cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest
	ManagerClient() manager.ManagerClient
	ManagerVersion() semver.Version
	SessionInfo() *manager.SessionInfo
	SetFileSharingPorts(ftp uint16, sftp uint16)
	SetManager(ctx context.Context, sessionInfo *manager.SessionInfo, manager manager.ManagerClient, version semver.Version)
	FtpPort() uint16
	SftpPort() uint16
}

type SimpleState interface {
	State
	NewInterceptState(forwarder forwarder.Interceptor, target InterceptTarget, mountPoint string, env map[string]string) InterceptState
}

// An InterceptState implements what's needed to intercept one target port.
type InterceptState interface {
	State
	Target() InterceptTarget
	InterceptInfo(ctx context.Context, callerID, path string, containerPort uint16, headers http.Header) (*restapi.InterceptInfo, error)
}

// State of the Traffic Agent.
type state struct {
	Config
	ftpPort          uint16
	sftpPort         uint16
	dialWatchers     *xsync.MapOf[string, chan *manager.DialRequest]
	awaitingForwards *xsync.MapOf[string, *xsync.MapOf[tunnel.ConnID, *awaitingForward]]

	// The sessionInfo and manager client are needed when forwarders establish their
	// tunnel to the traffic-manager.
	sessionInfo *manager.SessionInfo
	manager     manager.ManagerClient
	mgrVer      semver.Version

	interceptStates []InterceptState
	agent.UnimplementedAgentServer
}

type simpleState struct {
	state
	chosenIntercept *manager.InterceptInfo
}

func (s *state) ManagerClient() manager.ManagerClient {
	return s.manager
}

func (s *state) ManagerVersion() semver.Version {
	return s.mgrVer
}

func (s *state) SetFileSharingPorts(ftp uint16, sftp uint16) {
	s.ftpPort = ftp
	s.sftpPort = sftp
}

func (s *state) SessionInfo() *manager.SessionInfo {
	return s.sessionInfo
}

func NewState(config Config) State {
	return &state{
		Config:           config,
		dialWatchers:     xsync.NewMapOf[string, chan *manager.DialRequest](),
		awaitingForwards: xsync.NewMapOf[string, *xsync.MapOf[tunnel.ConnID, *awaitingForward]](),
	}
}

func NewSimpleState(config Config) SimpleState {
	return &simpleState{state: state{
		Config:           config,
		dialWatchers:     xsync.NewMapOf[string, chan *manager.DialRequest](),
		awaitingForwards: xsync.NewMapOf[string, *xsync.MapOf[tunnel.ConnID, *awaitingForward]](),
	}}
}

func (s *state) AddInterceptState(is InterceptState) {
	s.interceptStates = append(s.interceptStates, is)
}

func (s *state) AgentState() restapi.AgentState {
	return s
}

func (s *state) InterceptStates() []InterceptState {
	return s.interceptStates
}

func (s *state) HandleIntercepts(ctx context.Context, iis []*manager.InterceptInfo) []*manager.ReviewInterceptRequest {
	var rs []*manager.ReviewInterceptRequest
	for _, ist := range s.interceptStates {
		ms := make([]*manager.InterceptInfo, 0, len(iis))
		for _, ii := range iis {
			ic := ist.Target()
			if ic.MatchForSpec(ii.Spec) {
				dlog.Debugf(ctx, "intercept id %s svc=%q, svcPortId=%q matches target protocol=%s, agentPort=%d, containerPort=%d",
					ii.Id, ii.Spec.ServiceName, ii.Spec.ServicePortIdentifier, ic.Protocol(), ic.AgentPort(), ic.ContainerPort())
				ms = append(ms, ii)
			}
		}
		rs = append(rs, ist.HandleIntercepts(ctx, ms)...)
	}
	return rs
}

func (s *simpleState) HandleIntercepts(ctx context.Context, iis []*manager.InterceptInfo) []*manager.ReviewInterceptRequest {
	if s.chosenIntercept != nil {
		chosenID := s.chosenIntercept.Id
		found := false
		for _, is := range iis {
			if chosenID == is.Id {
				found = true
				s.chosenIntercept = is
				break
			}
		}
		if !found {
			// Chosen intercept is not present in the snapshot
			s.chosenIntercept = nil
		}
	}
	return s.state.HandleIntercepts(ctx, iis)
}

func (s *state) InterceptInfo(ctx context.Context, callerID, path string, containerPort uint16, headers http.Header) (*restapi.InterceptInfo, error) {
	if containerPort == 0 && len(s.interceptStates) == 1 {
		containerPort = s.interceptStates[0].Target().ContainerPort()
	}
	for _, is := range s.interceptStates {
		ic := is.Target()
		if containerPort == ic.ContainerPort() && ic.Protocol() == core.ProtocolTCP {
			return is.InterceptInfo(ctx, callerID, path, containerPort, headers)
		}
	}

	return &restapi.InterceptInfo{}, nil
}

func (s *state) SetManager(_ context.Context, sessionInfo *manager.SessionInfo, manager manager.ManagerClient, version semver.Version) {
	s.manager = manager
	s.sessionInfo = sessionInfo
	s.mgrVer = version
}

func (s *state) FtpPort() uint16 {
	return s.ftpPort
}

func (s *state) SftpPort() uint16 {
	return s.sftpPort
}

package agent

import (
	"context"
	"net/http"

	"github.com/blang/semver"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

// State reflects the current state of the agent.
type State interface {
	Config
	AddInterceptState(is InterceptState)
	AgentState() restapi.AgentState
	InterceptStates() []InterceptState
	HandleIntercepts(ctx context.Context, cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest
	ManagerClient() manager.ManagerClient
	ManagerVersion() semver.Version
	SessionInfo() *manager.SessionInfo
	SetManager(sessionInfo *manager.SessionInfo, manager manager.ManagerClient, version semver.Version)
	FtpPort() uint16
	SftpPort() uint16
	WaitForFtpPort(ctx context.Context, ch <-chan uint16) error
	WaitForSftpPort(ctx context.Context, ch <-chan uint16) error
}

// An InterceptState implements what's needed to intercept one port.
type InterceptState interface {
	State
	InterceptConfigs() []*agentconfig.Intercept
	InterceptInfo(ctx context.Context, callerID, path string, containerPort uint16, headers http.Header) (*restapi.InterceptInfo, error)
	HandleIntercepts(ctx context.Context, cepts []*manager.InterceptInfo) []*manager.ReviewInterceptRequest
}

// State of the Traffic Agent.
type state struct {
	Config
	ftpPort  uint16
	sftpPort uint16

	// The sessionInfo and manager client are needed when forwarders establish their
	// tunnel to the traffic-manager.
	sessionInfo *manager.SessionInfo
	manager     manager.ManagerClient
	mgrVer      semver.Version

	interceptStates []InterceptState
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

func (s *state) SessionInfo() *manager.SessionInfo {
	return s.sessionInfo
}

func NewState(config Config) State {
	return &state{Config: config}
}

func NewSimpleState(config Config) State {
	return &simpleState{state: state{Config: config}}
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
			for _, ic := range ist.InterceptConfigs() {
				if agentconfig.SpecMatchesIntercept(ii.Spec, ic) {
					dlog.Debugf(ctx, "intercept id %s svc=%q, svcPortId=%q matches config svc=%q, svcPort=%d, protocol=%s",
						ii.Id, ii.Spec.ServiceName, ii.Spec.ServicePortIdentifier, ic.ServiceName, ic.ServicePort, ic.Protocol)
					ms = append(ms, ii)
					break // Break inner loop, we don't want to add ii more than once
				}
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
	for _, is := range s.interceptStates {
		if containerPort == 0 || containerPort == is.InterceptConfigs()[0].ContainerPort {
			return is.InterceptInfo(ctx, callerID, path, containerPort, headers)
		}
	}

	return &restapi.InterceptInfo{}, nil
}

func (s *state) SetManager(sessionInfo *manager.SessionInfo, manager manager.ManagerClient, version semver.Version) {
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

func (s *state) WaitForFtpPort(ctx context.Context, ch <-chan uint16) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.ftpPort = <-ch:
		return nil
	}
}

func (s *state) WaitForSftpPort(ctx context.Context, ch <-chan uint16) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.sftpPort = <-ch:
		return nil
	}
}

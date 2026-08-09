package manager

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/state"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// externalService exposes only the client-facing subset of the wrapped
// Service to an externally reachable listener; methods with a broader
// internal request form return codes.Unimplemented or add an ownership
// check the wrapped method lacks.
type externalService struct {
	// UnsafeManagerServer opts out of the generated default bodies, so a
	// proto change adding a method fails the build until classified here.
	rpc.UnsafeManagerServer
	inner Service
}

var _ rpc.ManagerServer = (*externalService)(nil)

// newExternalService returns an rpc.ManagerServer that wraps inner with the
// client-only surface intended for the external TLS listener.
func newExternalService(inner Service) *externalService {
	return &externalService{inner: inner}
}

// requireAuthenticated rejects a call with no verified principal; defense
// in depth, since the listener's own interceptor already authenticates.
func requireAuthenticated(ctx context.Context) error {
	if auth.PrincipalFrom(ctx) == nil {
		return status.Error(codes.Unauthenticated, "this method requires an authenticated caller")
	}
	return nil
}

// internalOnly rejects an RPC this listener never serves.
func internalOnly(method string) error {
	return status.Errorf(codes.Unimplemented, "%s is only served on the traffic-manager's internal listener", method)
}

// ensureOwnedSession verifies session names a client session owned by the
// caller. A blank id is rejected outright, since several internal handlers
// treat it as a request to act on every session.
func (s *externalService) ensureOwnedSession(ctx context.Context, session *rpc.SessionInfo) error {
	sessionID := session.GetSessionId()
	if sessionID == "" {
		return status.Error(codes.InvalidArgument, "a session id is required")
	}
	cs := s.inner.State().GetClient(tunnel.SessionID(sessionID))
	if cs == nil {
		return status.Errorf(codes.NotFound, "client session %q not found", sessionID)
	}
	return state.ClientOwnershipError(ctx, tunnel.SessionID(sessionID), cs)
}

// Version is the only method served before a caller is authenticated,
// alongside the health service registered separately.
func (s *externalService) Version(ctx context.Context, e *empty.Empty) (*rpc.VersionInfo2, error) {
	return s.inner.Version(ctx, e)
}

func (s *externalService) GetAgentImageFQN(ctx context.Context, e *empty.Empty) (*rpc.AgentImageFQN, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.GetAgentImageFQN(ctx, e)
}

func (s *externalService) GetAgentConfig(ctx context.Context, request *rpc.AgentConfigRequest) (*rpc.AgentConfigResponse, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.GetAgentConfig(ctx, request)
}

// GetClientConfig requires the caller's principal to already own at least
// one active client session, since there's no session argument to check
// ownership against.
func (s *externalService) GetClientConfig(ctx context.Context, e *empty.Empty) (*rpc.CLIConfig, error) {
	p := auth.PrincipalFrom(ctx)
	if p == nil {
		return nil, status.Error(codes.Unauthenticated, "this method requires an authenticated caller")
	}
	owned := false
	s.inner.State().EachClient(func(_ tunnel.SessionID, cs *state.ClientSession) bool {
		if cs.Principal().SameAs(p) {
			owned = true
			return false
		}
		return true
	})
	if !owned {
		return nil, status.Error(codes.PermissionDenied, "no active client session owned by this caller")
	}
	return s.inner.GetClientConfig(ctx, e)
}

func (s *externalService) GetTelepresenceAPI(ctx context.Context, e *empty.Empty) (*rpc.TelepresenceAPIInfo, error) {
	return nil, internalOnly("GetTelepresenceAPI")
}

func (s *externalService) ArriveAsClient(ctx context.Context, client *rpc.ClientInfo) (*rpc.SessionInfo, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.ArriveAsClient(ctx, client)
}

func (s *externalService) ReconnectAgent(ctx context.Context, rq *rpc.ReconnectAgentRequest) (*empty.Empty, error) {
	return nil, internalOnly("ReconnectAgent")
}

func (s *externalService) ReconnectClient(ctx context.Context, info *rpc.ReconnectClientRequest) (*empty.Empty, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.ReconnectClient(ctx, info)
}

func (s *externalService) ArriveAsAgent(ctx context.Context, agentInfo *rpc.AgentInfo) (*rpc.SessionInfo, error) {
	return nil, internalOnly("ArriveAsAgent")
}

func (s *externalService) Remain(ctx context.Context, req *rpc.RemainRequest) (*empty.Empty, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.Remain(ctx, req)
}

func (s *externalService) Depart(ctx context.Context, session *rpc.SessionInfo) (*empty.Empty, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.Depart(ctx, session)
}

func (s *externalService) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (*empty.Empty, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.SetLogLevel(ctx, request)
}

func (s *externalService) StreamLogs(request *rpc.StreamLogsRequest, stream grpc.ServerStreamingServer[rpc.LogChunk]) error {
	if err := requireAuthenticated(stream.Context()); err != nil {
		return err
	}
	return s.inner.StreamLogs(request, stream)
}

func (s *externalService) WatchAgentPods(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentPodInfoSnapshot]) error {
	if err := requireAuthenticated(stream.Context()); err != nil {
		return err
	}
	return s.inner.WatchAgentPods(session, stream)
}

func (s *externalService) WatchAgentPodsDelta(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentPodInfoDelta]) error {
	if err := requireAuthenticated(stream.Context()); err != nil {
		return err
	}
	return s.inner.WatchAgentPodsDelta(session, stream)
}

func (s *externalService) WatchAgentPodsInNamespacesDelta(
	request *rpc.AgentsRequest, stream grpc.ServerStreamingServer[rpc.AgentPodInfoDelta],
) error {
	if err := requireAuthenticated(stream.Context()); err != nil {
		return err
	}
	return s.inner.WatchAgentPodsInNamespacesDelta(request, stream)
}

func (s *externalService) WatchAgents(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentInfoSnapshot]) error {
	if err := requireAuthenticated(stream.Context()); err != nil {
		return err
	}
	return s.inner.WatchAgents(session, stream)
}

func (s *externalService) WatchAgentsDelta(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.AgentInfoDelta]) error {
	if err := requireAuthenticated(stream.Context()); err != nil {
		return err
	}
	return s.inner.WatchAgentsDelta(session, stream)
}

// WatchIntercepts, given a blank session id, has the wrapped Service watch
// every non-child intercept in the manager; the external listener must
// never accept that form, so a session and its ownership are required here.
func (s *externalService) WatchIntercepts(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.InterceptInfoSnapshot]) error {
	ctx := stream.Context()
	if err := requireAuthenticated(ctx); err != nil {
		return err
	}
	if err := s.ensureOwnedSession(ctx, session); err != nil {
		return err
	}
	return s.inner.WatchIntercepts(session, stream)
}

// WatchInterceptsDelta shares WatchIntercepts' internal all-intercepts
// behavior on a blank session id; the same hardening applies.
func (s *externalService) WatchInterceptsDelta(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.InterceptInfoDelta]) error {
	ctx := stream.Context()
	if err := requireAuthenticated(ctx); err != nil {
		return err
	}
	if err := s.ensureOwnedSession(ctx, session); err != nil {
		return err
	}
	return s.inner.WatchInterceptsDelta(session, stream)
}

func (s *externalService) WatchSessionEvents(request *rpc.SessionEventsRequest, stream grpc.ServerStreamingServer[rpc.SessionEventsDelta]) error {
	if err := requireAuthenticated(stream.Context()); err != nil {
		return err
	}
	return s.inner.WatchSessionEvents(request, stream)
}

// WatchWorkloads' wrapped implementation already verifies session ownership
// and, for an explicitly named namespace, the authorized-namespace probe.
func (s *externalService) WatchWorkloads(request *rpc.WorkloadEventsRequest, stream grpc.ServerStreamingServer[rpc.WorkloadEventsDelta]) error {
	if err := requireAuthenticated(stream.Context()); err != nil {
		return err
	}
	return s.inner.WatchWorkloads(request, stream)
}

// WatchClusterInfo's internal handler checks that the session exists but
// not that the caller owns it.
func (s *externalService) WatchClusterInfo(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.ClusterInfo]) error {
	ctx := stream.Context()
	if err := requireAuthenticated(ctx); err != nil {
		return err
	}
	if err := s.ensureOwnedSession(ctx, session); err != nil {
		return err
	}
	return s.inner.WatchClusterInfo(session, stream)
}

func (s *externalService) WatchNamespaces(session *rpc.SessionInfo, stream grpc.ServerStreamingServer[rpc.NamespaceList]) error {
	if err := requireAuthenticated(stream.Context()); err != nil {
		return err
	}
	return s.inner.WatchNamespaces(session, stream)
}

func (s *externalService) EnsureAgent(ctx context.Context, request *rpc.EnsureAgentRequest) (*rpc.AgentInfoSnapshot, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.EnsureAgent(ctx, request)
}

func (s *externalService) ReleaseAgent(ctx context.Context, request *rpc.ReleaseAgentRequest) (*empty.Empty, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.ReleaseAgent(ctx, request)
}

func (s *externalService) PrepareIntercept(ctx context.Context, request *rpc.CreateInterceptRequest) (*rpc.PreparedIntercept, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.PrepareIntercept(ctx, request)
}

func (s *externalService) CreateIntercept(ctx context.Context, ciReq *rpc.CreateInterceptRequest) (*rpc.InterceptInfo, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.CreateIntercept(ctx, ciReq)
}

func (s *externalService) RemoveIntercept(ctx context.Context, riReq *rpc.RemoveInterceptRequest2) (*empty.Empty, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.RemoveIntercept(ctx, riReq)
}

func (s *externalService) GetIntercept(ctx context.Context, request *rpc.GetInterceptRequest) (*rpc.InterceptInfo, error) {
	return nil, internalOnly("GetIntercept")
}

func (s *externalService) ReviewIntercept(ctx context.Context, rIReq *rpc.ReviewInterceptRequest) (*empty.Empty, error) {
	return nil, internalOnly("ReviewIntercept")
}

// GetKnownWorkloadKinds' internal handler never inspects its SessionInfo
// argument at all, so ownership is checked here instead.
func (s *externalService) GetKnownWorkloadKinds(ctx context.Context, request *rpc.SessionInfo) (*rpc.KnownWorkloadKinds, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if err := s.ensureOwnedSession(ctx, request); err != nil {
		return nil, err
	}
	return s.inner.GetKnownWorkloadKinds(ctx, request)
}

func (s *externalService) Lookup(ctx context.Context, request *rpc.LookupRequest) (*rpc.LookupResponse, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.Lookup(ctx, request)
}

// LookupDNS's internal handler checks ownership only when the session
// resolves to a known client, and otherwise falls through unrestricted.
func (s *externalService) LookupDNS(ctx context.Context, request *rpc.DNSRequest) (*rpc.DNSResponse, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if err := s.ensureOwnedSession(ctx, request.GetSession()); err != nil {
		return nil, err
	}
	return s.inner.LookupDNS(ctx, request)
}

func (s *externalService) ResolveServicePort(ctx context.Context, request *rpc.ResolveServicePortRequest) (*rpc.ResolveServicePortResponse, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.ResolveServicePort(ctx, request)
}

func (s *externalService) WatchLogLevel(e *empty.Empty, stream grpc.ServerStreamingServer[rpc.LogLevelRequest]) error {
	return internalOnly("WatchLogLevel")
}

// Tunnel's internal handler resolves the declared session against the
// client-session map only, already rejecting an agent session id (a
// separate map) and enforcing ownership, so no extra wrapping is needed.
func (s *externalService) Tunnel(server grpc.BidiStreamingServer[rpc.TunnelMessage, rpc.TunnelMessage]) error {
	if err := requireAuthenticated(server.Context()); err != nil {
		return err
	}
	return s.inner.Tunnel(server)
}

func (s *externalService) GetQuicTunnelEndpoint(ctx context.Context, session *rpc.SessionInfo) (*rpc.QuicTunnelEndpoint, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.GetQuicTunnelEndpoint(ctx, session)
}

func (s *externalService) GetSessionCredential(ctx context.Context, session *rpc.SessionInfo) (*rpc.SessionCredential, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	return s.inner.GetSessionCredential(ctx, session)
}

func (s *externalService) GetQuicAgentCert(ctx context.Context, session *rpc.SessionInfo) (*rpc.QuicAgentCert, error) {
	return nil, internalOnly("GetQuicAgentCert")
}

func (s *externalService) WatchQuicBackends(e *empty.Empty, stream grpc.ServerStreamingServer[rpc.QuicBackendSnapshot]) error {
	return internalOnly("WatchQuicBackends")
}

func (s *externalService) ReportMetrics(ctx context.Context, metrics *rpc.TunnelMetrics) (*empty.Empty, error) {
	return nil, internalOnly("ReportMetrics")
}

// UninstallAgents' internal handler only checks that the session exists,
// not that the caller owns it.
func (s *externalService) UninstallAgents(ctx context.Context, request *rpc.UninstallAgentsRequest) (*empty.Empty, error) {
	if err := requireAuthenticated(ctx); err != nil {
		return nil, err
	}
	if err := s.ensureOwnedSession(ctx, request.GetSessionInfo()); err != nil {
		return nil, err
	}
	return s.inner.UninstallAgents(ctx, request)
}

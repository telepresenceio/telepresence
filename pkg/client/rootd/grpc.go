package rootd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/rootd/dns"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func (s *service) Version(ctx context.Context, _ *emptypb.Empty) (*common.VersionInfo, error) {
	return client.VersionInfo(ctx), nil
}

func (s *service) Status(ctx context.Context, _ *emptypb.Empty) (*rpc.DaemonStatus, error) {
	s.sessionLock.RLock()
	defer s.sessionLock.RUnlock()
	r := &rpc.DaemonStatus{
		Managed: s.managed,
		Version: client.VersionInfo(ctx),
	}
	if s.session != nil {
		r.OutboundConfig = s.session.getNetworkConfig()
	}
	return r, nil
}

func (s *service) Quit(ctx context.Context, _ *emptypb.Empty) (*rpc.QuitResponse, error) {
	s.cancelSession(ctx)
	s.quit()
	return &rpc.QuitResponse{RootDaemonWillContinue: s.managed}, nil
}

func (s *service) SetDNSTopLevelDomains(ctx context.Context, domains *rpc.Domains) (*emptypb.Empty, error) {
	err := s.withSession(ctx, func(_ context.Context, session *session) error {
		session.SetTopLevelDomains(domains.Domains)
		return nil
	})
	return &emptypb.Empty{}, err
}

func (s *service) SetDNSExcludes(ctx context.Context, req *rpc.SetDNSExcludesRequest) (*emptypb.Empty, error) {
	err := s.withSession(ctx, func(_ context.Context, session *session) error {
		session.SetExcludes(req.Excludes)
		return nil
	})
	return &emptypb.Empty{}, err
}

func (s *service) SetDNSMappings(ctx context.Context, req *rpc.SetDNSMappingsRequest) (*emptypb.Empty, error) {
	err := s.withSession(ctx, func(_ context.Context, session *session) error {
		session.SetMappings(req.Mappings)
		return nil
	})
	return &emptypb.Empty{}, err
}

func (s *service) Connect(ctx context.Context, info *rpc.NetworkConfig) (reply *rpc.DaemonStatus, err error) {
	reply = &rpc.DaemonStatus{Version: client.VersionInfo(ctx)}
	err = s.withSession(ctx, func(_ context.Context, session *session) error {
		reply.OutboundConfig = s.session.getNetworkConfig()
		return nil
	})
	if err == nil {
		return reply, nil
	}

	s.sessionLock.Lock()
	defer s.sessionLock.Unlock()
	if s.session != nil {
		// Someone took the lock before we did and created a session.k
		reply.OutboundConfig = s.session.getNetworkConfig()
		return reply, nil
	}

	cfg, err := client.UnmarshalJSONConfig(info.ClientConfig, false)
	if err != nil {
		return nil, err
	}

	sessionCtx, sessionCancel := context.WithCancelCause(s)
	var sn *session
	sn, err = createSession(client.WithConfig(sessionCtx, cfg), ctx, info, s.activity, dns.CleanupRouting)
	if err != nil {
		sessionCancel(fmt.Errorf("failed to create root daemon session: %w", err))
		return nil, err
	}
	if !s.managed {
		// Only reload log level from client config if not running as a managed service.
		// A managed service should maintain its own log level configuration.
		client.ReloadLogLevel(sn)
	}
	reply.OutboundConfig = sn.getNetworkConfig()
	initErrCh := make(chan error, 1)

	sessionRunning := make(chan struct{})
	go func() {
		defer func() {
			sessionCancel(context.Canceled)
			if !s.managed {
				// Restore log level from service config after session ends.
				client.ReloadLogLevel(s)
			}
			close(sessionRunning)
		}()
		sn.run(initErrCh)
		s.clearSession(sn)
	}()
	select {
	case <-sn.Done():
		// Session (or service) was canceled.
		return nil, status.Error(codes.Canceled, "session canceled")
	case <-ctx.Done():
		// gRPC context was canceled, probably by the caller.
		sessionCancel(errors.New("root daemon Connect call canceled"))
		return nil, status.Error(codes.Canceled, "connect call canceled")
	case err = <-initErrCh:
		if err != nil {
			// Session failed to initialize.
			sessionCancel(fmt.Errorf("root daemon session initialization failed: %w", err))
			return nil, err
		}
		// Session initialized successfully.
	}
	s.session = sn
	s.sessionCancel = func(cause error) {
		if cause == nil {
			cause = context.Canceled
		}
		clog.Infof(sn, "canceling root daemon session: %v", cause)
		sessionCancel(cause)
	}
	s.sessionRunning = sessionRunning
	return reply, nil
}

func (s *service) Disconnect(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	s.cancelSession(ctx)
	return &emptypb.Empty{}, nil
}

func (s *service) clearSession(oldSession *session) {
	s.sessionLock.Lock()
	if s.session == oldSession {
		s.session = nil
		s.sessionCancel = nil
	}
	s.sessionLock.Unlock()
}

func (s *service) cancelSession(ctx context.Context) {
	// We must use a shared read lock when cancelling to avoid a deadlock.
	var oldSession *session
	err := s.withSession(ctx, func(_ context.Context, session *session) error {
		s.sessionCancel(errors.New("root daemon Disconnect request"))
		oldSession = session
		return nil
	})
	if err == nil {
		// Session is officially dead, and we don't want anyone to use during the time when it's shutting down.
		s.clearSession(oldSession)
	}
}

func (s *service) TranslateEnvIPs(ctx context.Context, environment *rpc.Environment) (result *rpc.Environment, err error) {
	err = s.withSession(ctx, func(_ context.Context, session *session) error {
		result = session.translateEnvIPs(environment)
		return nil
	})
	return result, err
}

func (s *service) WaitForNetwork(ctx context.Context, _ *emptypb.Empty) (*emptypb.Empty, error) {
	err := s.withSession(ctx, func(ctx context.Context, session *session) error {
		if err, ok := <-session.networkReady(ctx); ok {
			return status.Error(codes.Unavailable, err.Error())
		}
		return nil
	})
	return &emptypb.Empty{}, err
}

func (s *service) GetNetworkConfig(ctx context.Context, _ *emptypb.Empty) (nc *rpc.NetworkConfig, err error) {
	err = s.withSession(ctx, func(_ context.Context, session *session) error {
		nc = session.getNetworkConfig()
		return nil
	})
	return nc, err
}

func (s *service) WaitForAgentIP(ctx context.Context, request *rpc.WaitForAgentIPRequest) (rsp *rpc.WaitForAgentIPResponse, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session *session) error {
		rsp, err = session.waitForAgentIP(ctx, request)
		return err
	})
	return rsp, err
}

func (s *service) SetLogLevel(ctx context.Context, request *manager.LogLevelRequest) (*emptypb.Empty, error) {
	lvl, err := clog.ParseLevel(request.LogLevel)
	if err != nil {
		return &emptypb.Empty{}, status.Error(codes.InvalidArgument, err.Error())
	}
	duration := time.Duration(0)
	if request.Duration != nil {
		duration = request.Duration.AsDuration()
	}
	return &emptypb.Empty{}, logging.SetAndStoreTimedLevel(ctx, s.timedLogLevel, lvl, duration, client.RootDaemonName)
}

func (s *service) LookupIP(ctx context.Context, request *rpc.LookupIPRequest) (rsp *rpc.LookupIPResponse, err error) {
	err = s.withSession(ctx, func(_ context.Context, session *session) error {
		rsp, err = session.lookupIP(request)
		return err
	})
	return rsp, err
}

func (s *service) ResolvePort(ctx context.Context, request *rpc.ResolvePortRequest) (rsp *rpc.ResolvePortResponse, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session *session) error {
		ap, err := session.resolvePort(ctx, request.Host, request.Port)
		if err != nil {
			return err
		}
		apb, err := ap.MarshalBinary()
		if err != nil {
			return err
		}
		rsp = &rpc.ResolvePortResponse{HostPort: apb}
		return nil
	})
	return rsp, err
}

func (s *service) RerouteRemotePort(ctx context.Context, request *rpc.ReroutePortRequest) (rsp *emptypb.Empty, err error) {
	err = s.withSession(ctx, func(_ context.Context, session *session) error {
		var ap types.AddrPortProto
		err = ap.UnmarshalBinary(request.DstHostPort)
		if err == nil {
			session.rerouteRemotePort(ap, uint16(request.SrcPort))
		}
		return err
	})
	return &emptypb.Empty{}, err
}

func (s *service) withSession(ctx context.Context, f func(context.Context, *session) error) (err error) {
	s.sessionLock.RLock()
	defer s.sessionLock.RUnlock()
	if s.session == nil {
		return status.Error(codes.Unavailable, "no active session")
	}
	select {
	case <-s.session.Done():
		return status.Error(codes.Canceled, "session canceled")
	default:
		return f(server.NewCombinedContext(s.session, ctx), s.session)
	}
}

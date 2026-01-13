package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/bwcompat"
	cliDaemon "github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	grpcErrors "github.com/telepresenceio/telepresence/v2/pkg/grpc/errors"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func (s *service) FuseFTPError() error {
	return s.fuseFTPError
}

func (s *service) withSession(ctx context.Context, f func(context.Context, userd.Session) error) (err error) {
	s.sessionLock.RLock()
	defer s.sessionLock.RUnlock()
	if s.session == nil {
		return status.Error(codes.Unavailable, "no active session")
	}
	select {
	case <-s.session.Done():
		return status.Error(codes.Canceled, "session cancelled")
	default:
		return f(server.NewCombinedContext(s.session, ctx), s.session)
	}
}

func (s *service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	executable, err := client.Executable()
	if err != nil {
		return &common.VersionInfo{}, err
	}
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
		Executable: executable,
		Name:       client.DisplayName,
	}, nil
}

func (s *service) Connect(ctx context.Context, cr *rpc.ConnectRequest) (result *rpc.ConnectInfo, err error) {
	result = &rpc.ConnectInfo{}

	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) (err error) {
		result, err = session.UpdateStatus(ctx, cr)
		return err
	})
	if status.Code(err) != codes.Unavailable {
		return result, err
	}

	s.sessionLock.Lock()
	defer s.sessionLock.Unlock()
	if s.session != nil {
		// Someone beat us to taking the lock.
		err = s.session.CheckStatus(cr)
		if err != nil {
			return nil, err
		}
		return s.session.Status(server.NewCombinedContext(s.session, ctx))
	}

	cfg, err := client.LoadConfig(s)
	if err != nil {
		return nil, err
	}

	// Obtain the kubeconfig from the request parameters so that we can determine
	// what kubernetes context that will be used.
	sessionCtx, sessionCancel := context.WithCancel(s.Context)
	config, err := k8s.DaemonKubeconfig(client.WithConfig(sessionCtx, cfg), cr)
	if err != nil {
		sessionCancel()
		if s.rootSessionInProc {
			s.quit(true)
		}
		dlog.Errorf(ctx, "Failed to obtain kubeconfig: %v", err)
		return result, err
	}

	// The service must know about the clientConfig when the session is created because the session creation
	// will connect to the root daemon, which in turn might call back to the Authenticator service provided by
	// this service.
	s.clientConfigLock.Lock()
	s.clientConfig = config.ClientConfig
	s.clientConfigLock.Unlock()
	defer func() {
		if err != nil {
			s.clientConfigLock.Lock()
			s.clientConfig = nil
			s.clientConfigLock.Unlock()
		}
	}()

	daemonID := cliDaemon.NewIdentifier(cr.Name, config.KubeContext, config.Namespace, proc.RunningInContainer())
	wg := &sync.WaitGroup{}

	var session userd.Session
	session, result, err = trafficmgr.NewSession(s, server.NewCombinedContext(s, ctx), cr, config, wg)
	if err != nil {
		sessionCancel()
		if s.rootSessionInProc {
			// Simplified session management. The daemon handles one session, then exits.
			s.quit(true)
		}
		return nil, err
	}
	client.ReloadDaemonLogLevel(session)
	s.sessionCancel = func() {
		if err := session.ClearIngestsAndIntercepts(); err != nil {
			dlog.Errorf(ctx, "failed to clear intercepts: %v", err)
		}
		sessionCancel()
	}
	sessionRunning := make(chan struct{})
	s.session = session
	s.sessionRunning = sessionRunning

	// Run the session asynchronously. We must be able to respond to connect (with UpdateStatus) while
	// the session is running. The s.sessionCancel is called from Disconnect
	go func() {
		session.Run()
		wg.Wait()
		close(sessionRunning)
		if s.rootSessionInProc {
			// Simplified session management. The daemon handles one session, then exits.
			s.quit(false)
		}
		s.clearSession(session)
	}()
	go runAliveAndCancellation(session, s.sessionCancel, daemonID, wg)
	return result, err
}

func (s *service) Disconnect(ctx context.Context, ex *empty.Empty) (*empty.Empty, error) {
	s.cancelSession(ctx, true)
	return &empty.Empty{}, nil
}

func (s *service) cancelSession(ctx context.Context, disconnectRoot bool) {
	var oldSession userd.Session
	err := s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		oldSession = session
		s.sessionCancel()
		return nil
	})
	if err == nil && s.clearSession(oldSession) && disconnectRoot {
		_ = s.withRootDaemon(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
			_, _ = rd.Disconnect(ctx, &empty.Empty{})
			return nil
		})
	}
}

func (s *service) clearSession(oldSession userd.Session) bool {
	s.sessionLock.Lock()
	sameSession := s.session == oldSession
	if sameSession {
		s.session = nil
		s.sessionCancel = nil
		s.clientConfigLock.Lock()
		s.clientConfig = nil
		s.clientConfigLock.Unlock()
	}
	s.sessionLock.Unlock()
	client.ReloadDaemonLogLevel(s)
	return sameSession
}

func (s *service) Status(ctx context.Context, ex *empty.Empty) (result *rpc.ConnectInfo, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) (err error) {
		result, err = session.Status(ctx)
		return err
	})
	if status.Code(err) != codes.Unavailable {
		return result, err
	}
	err = s.withRootDaemon(ctx, func(c context.Context, dc daemon.DaemonClient) (err error) {
		if result == nil {
			// This may happen if the session was unavailable, which in this particular case is OK.
			result = &rpc.ConnectInfo{}
		}
		result.DaemonStatus, err = dc.Status(c, ex)
		return err
	})
	return result, err
}

func (s *service) CanIntercept(ctx context.Context, ir *rpc.CreateInterceptRequest) (empty2 *empty.Empty, err error) {
	return &empty.Empty{}, s.withSession(ctx, func(ctx context.Context, session userd.Session) (err error) {
		_, err = session.CanIntercept(ctx, ir)
		return err
	})
}

func (s *service) CreateIntercept(ctx context.Context, ir *rpc.CreateInterceptRequest) (result *manager.InterceptInfo, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) (err error) {
		result, err = session.AddIntercept(ctx, ir)
		return err
	})
	return result, err
}

func (s *service) RemoveIntercept(ctx context.Context, rr *manager.RemoveInterceptRequest2) (*empty.Empty, error) {
	return &empty.Empty{}, s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		return session.RemoveIntercept(rr.Name)
	})
}

func (s *service) RevokeIntercept(ctx context.Context, rr *manager.RevokeInterceptRequest) (*empty.Empty, error) {
	return &empty.Empty{}, s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		_, err := session.ManagerClient().RevokeIntercept(ctx, rr)
		return err
	})
}

func (s *service) AddInterceptor(ctx context.Context, interceptor *rpc.Interceptor) (*empty.Empty, error) {
	return &empty.Empty{}, s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		return session.AddInterceptor(interceptor.InterceptId, interceptor)
	})
}

func (s *service) RemoveInterceptor(ctx context.Context, interceptor *rpc.Interceptor) (*empty.Empty, error) {
	return &empty.Empty{}, s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		return session.RemoveInterceptor(interceptor.InterceptId)
	})
}

func (s *service) List(ctx context.Context, lr *rpc.ListRequest) (result *rpc.WorkloadInfoSnapshot, err error) {
	err = s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		result, err = session.WorkloadInfoSnapshot([]string{lr.Namespace}, lr.Filter)
		return err
	})
	return result, err
}

func (s *service) GetKnownWorkloadKinds(ctx context.Context, _ *empty.Empty) (result *manager.KnownWorkloadKinds, err error) {
	err = s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		result, err = session.ManagerClient().GetKnownWorkloadKinds(ctx, session.SessionInfo())
		if err != nil {
			if status.Code(err) != codes.Unimplemented {
				return err
			}
			// Talking to an older traffic-manager, use legacy default types
			result = &manager.KnownWorkloadKinds{Kinds: []manager.WorkloadInfo_Kind{
				manager.WorkloadInfo_DEPLOYMENT,
				manager.WorkloadInfo_REPLICASET,
				manager.WorkloadInfo_STATEFULSET,
			}}
		}
		return nil
	})
	return result, err
}

func (s *service) WatchWorkloads(wr *rpc.WatchWorkloadsRequest, stream rpc.Connector_WatchWorkloadsServer) error {
	var session userd.Session
	err := s.withSession(stream.Context(), func(_ context.Context, s userd.Session) error {
		session = s
		return nil
	})
	if err != nil {
		return err
	}

	return session.WatchWorkloads(wr, stream)
}

func (s *service) Uninstall(ctx context.Context, ur *rpc.UninstallRequest) (*empty.Empty, error) {
	return &empty.Empty{}, s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		return session.Uninstall(ctx, ur)
	})
}

func (s *service) GetConfig(ctx context.Context, _ *empty.Empty) (cfg *rpc.ClientConfig, err error) {
	err = s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		sc, err := session.GetConfig()
		if err != nil {
			return err
		}
		data, err := json.Marshal(sc)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		cfg = &rpc.ClientConfig{Json: data}
		return nil
	})
	return cfg, err
}

func (s *service) GatherLogs(ctx context.Context, request *rpc.LogsRequest) (result *rpc.LogsResponse, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		result, err = session.GatherLogs(ctx, request)
		return err
	})
	return result, err
}

func (s *service) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (result *empty.Empty, err error) {
	mrq := &manager.LogLevelRequest{
		LogLevel: request.LogLevel,
		Duration: request.Duration,
	}
	setLocal := func() {
		duration := time.Duration(0)
		if request.Duration != nil {
			duration = request.Duration.AsDuration()
		}
		if err = logging.SetAndStoreTimedLevel(ctx, s.timedLogLevel, request.LogLevel, duration, client.UserDaemonName); err != nil {
			err = status.Error(codes.Internal, err.Error())
		} else if !s.rootSessionInProc {
			err = s.withRootDaemon(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
				_, err := rd.SetLogLevel(ctx, mrq)
				return err
			})
		}
	}
	setRemote := func() {
		err = s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
			_, err := session.ManagerClient().SetLogLevel(ctx, mrq)
			return err
		})
	}
	switch request.Scope {
	case rpc.LogLevelRequest_LOCAL_ONLY:
		setLocal()
	case rpc.LogLevelRequest_REMOTE_ONLY:
		setRemote()
	default:
		setLocal()
		if err == nil {
			setRemote()
		}
	}
	return &empty.Empty{}, err
}

func (s *service) Quit(ctx context.Context, ex *empty.Empty) (*empty.Empty, error) {
	s.cancelSession(ctx, false)
	s.quit(false)
	_ = s.withRootDaemon(context.WithoutCancel(ctx), func(ctx context.Context, rd daemon.DaemonClient) error {
		dlog.Debug(ctx, "Telling root daemon to Quit")
		_, err := rd.Quit(ctx, ex)
		return err
	})
	return ex, nil
}

func (s *service) RemoteMountAvailability(ctx context.Context, ex *empty.Empty) (*empty.Empty, error) {
	if proc.RunningInContainer() {
		// We mount using docker volumes and the telemount driver plugin.
		return ex, nil
	}
	if client.GetConfig(ctx).Intercept().UseFtp {
		return ex, s.FuseFTPError()
	}

	// Use CombinedOutput to include stderr which has information about whether they
	// need to upgrade to a newer version of macFUSE or not
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = proc.CommandContext(ctx, "sshfs-win", "cmd", "-V")
	} else {
		cmd = proc.CommandContext(ctx, "sshfs", "-V")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		dlog.Errorf(ctx, "sshfs not installed: %v", err)
		return ex, errcat.User.New("sshfs is not installed on your local machine")
	}

	// OSXFUSE changed to macFUSE, and we've noticed that older versions of OSXFUSE
	// can cause browsers to hang + kernel crashes, so we add an error to prevent
	// our users from running into this problem.
	// OSXFUSE isn't included in the output of sshfs -V in versions of 4.0.0 so
	// we check for that as a proxy for if they have the right version or not.
	if bytes.Contains(out, []byte("OSXFUSE")) {
		return ex, errcat.User.New(`macFUSE 4.0.5 or higher is required on your local machine`)
	}
	return ex, nil
}

func (s *service) GetNamespaces(ctx context.Context, req *rpc.GetNamespacesRequest) (*rpc.GetNamespacesResponse, error) {
	var resp rpc.GetNamespacesResponse
	err := s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		resp.Namespaces = session.GetCurrentNamespaces(req.ForClientAccess)
		return nil
	})
	if err != nil {
		return nil, err
	}

	if p := req.Prefix; p != "" {
		var namespaces []string
		for _, namespace := range resp.Namespaces {
			if strings.HasPrefix(namespace, p) {
				namespaces = append(namespaces, namespace)
			}
		}
		resp.Namespaces = namespaces
	}

	return &resp, nil
}

func (s *service) TrafficManagerVersion(ctx context.Context, _ *empty.Empty) (vi *common.VersionInfo, err error) {
	err = s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		vi = &common.VersionInfo{Name: session.ManagerName(), Version: "v" + session.ManagerVersion().String()}
		return nil
	})
	return vi, err
}

func (s *service) RootDaemonVersion(ctx context.Context, empty *empty.Empty) (vi *common.VersionInfo, err error) {
	err = s.withRootDaemon(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
		vi, err = rd.Version(s, empty)
		return err
	})
	return vi, err
}

func (s *service) AgentImageFQN(ctx context.Context, empty *empty.Empty) (fqn *manager.AgentImageFQN, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		fqn, err = session.ManagerClient().GetAgentImageFQN(ctx, empty)
		return err
	})
	return fqn, err
}

func (s *service) GetAgentConfig(ctx context.Context, request *manager.AgentConfigRequest) (rsp *manager.AgentConfigResponse, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		request.Session = session.SessionInfo()
		rsp, err = session.ManagerClient().GetAgentConfig(ctx, request)
		return err
	})
	return rsp, err
}

func (s *service) GetClusterSubnets(ctx context.Context, _ *empty.Empty) (cs *rpc.ClusterSubnets, err error) {
	var podSubnets, svcSubnets []*manager.IPNet
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		// The manager can sometimes send the different subnets in different Sends,
		// but after 5 seconds of listening to it, we should expect to have everything
		tCtx, tCancel := context.WithTimeout(ctx, 5*time.Second)
		defer tCancel()
		infoStream, err := session.ManagerClient().WatchClusterInfo(tCtx, session.SessionInfo())
		if err != nil {
			return err
		}
		for {
			mgrInfo, err := infoStream.Recv()
			if err != nil {
				if tCtx.Err() != nil || errors.Is(err, io.EOF) {
					err = nil
				}
				return err
			}
			bwcompat.FixLegacyClusterInfo(mgrInfo)
			for _, sn := range mgrInfo.ServiceCidrs {
				var sb netip.Prefix
				err = sb.UnmarshalBinary(sn)
				if err != nil {
					return err
				}
				svcSubnets = append(svcSubnets, iputil.PrefixToRPC(sb))
			}
			podSubnets = append(podSubnets, mgrInfo.PodSubnets...)
		}
	})
	if err != nil {
		return nil, err
	}
	return &rpc.ClusterSubnets{PodSubnets: podSubnets, SvcSubnets: svcSubnets}, nil
}

func (s *service) GetIntercept(ctx context.Context, request *manager.GetInterceptRequest) (ii *manager.InterceptInfo, err error) {
	err = s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		ii = session.GetInterceptInfo(request.Name)
		if ii == nil {
			return status.Errorf(codes.NotFound, "found no intercept named %s", request.Name)
		}
		return nil
	})
	return ii, err
}

func (s *service) SetDNSExcludes(ctx context.Context, req *daemon.SetDNSExcludesRequest) (*empty.Empty, error) {
	err := s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		return session.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) (err error) {
			_, err = rd.SetDNSExcludes(ctx, req)
			return err
		})
	})
	return &empty.Empty{}, err
}

func (s *service) SetDNSMappings(ctx context.Context, req *daemon.SetDNSMappingsRequest) (*empty.Empty, error) {
	err := s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		return session.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) (err error) {
			_, err = rd.SetDNSMappings(ctx, req)
			return err
		})
	})
	return &empty.Empty{}, err
}

func (s *service) Ingest(ctx context.Context, request *rpc.IngestRequest) (response *rpc.IngestInfo, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		response, err = session.Ingest(ctx, request)
		return err
	})
	return response, err
}

func (s *service) GetIngest(ctx context.Context, request *rpc.IngestIdentifier) (response *rpc.IngestInfo, err error) {
	err = s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		response, err = session.GetIngest(request)
		return err
	})
	return response, err
}

func (s *service) LeaveIngest(ctx context.Context, request *rpc.IngestIdentifier) (response *rpc.IngestInfo, err error) {
	err = s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		response, err = session.LeaveIngest(request)
		return err
	})
	return response, err
}

func (s *service) ResolveSyntheticIP(ctx context.Context, request *rpc.ResolveSyntheticRequest) (response *rpc.ResolveSyntheticResponse, err error) {
	err = s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		ip, ok := netip.AddrFromSlice(request.Ip)
		if !ok {
			return status.Errorf(codes.InvalidArgument, "invalid IP")
		}
		n := session.ResolveName(ip)
		if n == "" {
			return status.Errorf(codes.NotFound, "found no match for synthetic IP %s", ip)
		}
		response = &rpc.ResolveSyntheticResponse{Name: n}
		if !request.NameOnly {
			ip, err = session.Resolve(ip)
			if err != nil {
				response = nil
				return status.Error(codes.NotFound, err.Error())
			}
			response.ResolvedIp = ip.AsSlice()
		}
		return nil
	})
	return response, err
}

func (s *service) LookupIP(ctx context.Context, request *daemon.LookupIPRequest) (rsp *daemon.LookupIPResponse, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		return session.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) (err error) {
			rsp, err = rd.LookupIP(ctx, request)
			return err
		})
	})
	return rsp, err
}

func (s *service) ResolvePort(ctx context.Context, request *daemon.ResolvePortRequest) (rsp *daemon.ResolvePortResponse, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		return session.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) (err error) {
			rsp, err = rd.ResolvePort(ctx, request)
			return err
		})
	})
	return rsp, err
}

func (s *service) RerouteLocalPort(ctx context.Context, request *daemon.ReroutePortRequest) (*empty.Empty, error) {
	err := s.withSession(ctx, func(_ context.Context, session userd.Session) error {
		var ap types.AddrPortProto
		if err := ap.UnmarshalBinary(request.DstHostPort); err != nil {
			return err
		}
		session.RerouteLocalPort(ap, uint16(request.SrcPort))
		return nil
	})
	return &empty.Empty{}, err
}

func (s *service) RerouteRemotePort(ctx context.Context, request *daemon.ReroutePortRequest) (rsp *empty.Empty, err error) {
	err = s.withSession(ctx, func(ctx context.Context, session userd.Session) error {
		return session.WithRootClient(ctx, func(ctx context.Context, rd daemon.DaemonClient) (err error) {
			rsp, err = rd.RerouteRemotePort(ctx, request)
			return err
		})
	})
	return rsp, err
}

func (s *service) withRootDaemon(ctx context.Context, f func(ctx context.Context, daemonClient daemon.DaemonClient) error) error {
	if s.rootSessionInProc {
		return status.Error(codes.Unavailable, "root daemon is embedded")
	}
	conn, err := socket.Dial(ctx, socket.RootDaemonPath(ctx), false)
	if err == nil {
		defer conn.Close()
		err = f(ctx, daemon.NewDaemonClient(conn))
	}
	if err != nil {
		err = grpcErrors.FromError(err, codes.Internal, fmt.Sprintf("root daemon: %s", err.Error()))
	}
	return err
}

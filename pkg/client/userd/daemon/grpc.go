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
	"sync/atomic"
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
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/iputil"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func (s *service) FuseFTPError() error {
	return s.fuseFTPError
}

func (s *service) WithSession(c context.Context, f func(context.Context, userd.Session) error) (err error) {
	if atomic.LoadInt32(&s.sessionQuitting) != 0 {
		return status.Error(codes.Canceled, "session cancelled")
	}
	s.sessionLock.RLock()
	defer s.sessionLock.RUnlock()
	if s.session == nil {
		return status.Error(codes.Unavailable, "no active session")
	}
	if s.sessionContext.Err() != nil {
		// Session context has been cancelled
		return status.Error(codes.Canceled, "session cancelled")
	}
	return f(s.sessionContext, s.session)
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

type crImpl struct {
	*rpc.ConnectRequest
}

func (c crImpl) Request() *rpc.ConnectRequest {
	return c.ConnectRequest
}

func (s *service) Connect(ctx context.Context, cr *rpc.ConnectRequest) (result *rpc.ConnectInfo, err error) {
	if err = s.PostConnectRequest(ctx, crImpl{ConnectRequest: cr}); err == nil {
		result, err = s.ReadConnectResponse(ctx)
	}
	return result, err
}

func (s *service) Disconnect(ctx context.Context, ex *empty.Empty) (*empty.Empty, error) {
	s.cancelSession()
	_ = s.withRootDaemon(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
		_, err := rd.Disconnect(ctx, ex)
		return err
	})
	return &empty.Empty{}, nil
}

func (s *service) Status(ctx context.Context, ex *empty.Empty) (result *rpc.ConnectInfo, err error) {
	s.sessionLock.RLock()
	defer s.sessionLock.RUnlock()
	if s.session == nil {
		result = &rpc.ConnectInfo{Error: rpc.ConnectInfo_DISCONNECTED}
		_ = s.withRootDaemon(ctx, func(c context.Context, dc daemon.DaemonClient) error {
			result.DaemonStatus, err = dc.Status(c, ex)
			return nil
		})
	} else {
		result = s.session.Status(s.sessionContext)
	}
	return
}

// isMultiPortIntercept checks if the intercept is one of several active intercepts on the same workload.
// If it is, then the first returned value will be true and the second will indicate if those intercepts are
// on different services. Otherwise, this function returns false, false.
func (s *service) isMultiPortIntercept(spec *manager.InterceptSpec) (multiPort, multiService bool) {
	wis := s.session.InterceptsForWorkload(spec.Agent, spec.Namespace)

	// The InterceptsForWorkload will not include failing or removed intercepts so the
	// subject must be added unless it's already there.
	active := false
	for _, is := range wis {
		if is.Name == spec.Name {
			active = true
			break
		}
	}
	if !active {
		wis = append(wis, spec)
	}
	if len(wis) < 2 {
		return false, false
	}
	var suid string
	for _, is := range wis {
		if suid == "" {
			suid = is.ServiceUid
		} else if suid != is.ServiceUid {
			return true, true
		}
	}
	return true, false
}

func (s *service) scoutInterceptEntries(ctx context.Context, spec *manager.InterceptSpec, result *rpc.InterceptResult) ([]scout.Entry, bool) {
	// The scout belongs to the session and can only contain session specific meta-data,
	// so we don't want to use scout.SetMetadatum() here.
	entries := make([]scout.Entry, 0, 7)
	if spec != nil {
		entries = append(entries,
			scout.Entry{Key: "service_name", Value: spec.ServiceName},
			scout.Entry{Key: "service_namespace", Value: spec.Namespace},
			scout.Entry{Key: "intercept_mechanism", Value: spec.Mechanism},
			scout.Entry{Key: "intercept_mechanism_numargs", Value: len(spec.Mechanism)},
		)
		multiPort, multiService := s.isMultiPortIntercept(spec)
		if multiPort {
			entries = append(entries, scout.Entry{Key: "multi_port", Value: multiPort})
			if multiService {
				entries = append(entries, scout.Entry{Key: "multi_service", Value: multiService})
			}
		}
	}
	if result != nil {
		entries = append(entries, scout.Entry{Key: "workload_kind", Value: result.WorkloadKind})
		if result.Error != common.InterceptError_UNSPECIFIED {
			es := result.Error.String()
			if result.ErrorText != "" {
				es = fmt.Sprintf("%s: %s", es, result.ErrorText)
			}
			dlog.Debugf(ctx, "reporting error: %s", es)
			entries = append(entries, scout.Entry{Key: "error", Value: es})
			return entries, false
		}
	}
	return entries, true
}

func (s *service) CanIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	var entries []scout.Entry
	ok := false
	defer func() {
		var action string
		if ok {
			action = "connector_can_intercept_success"
		} else {
			action = "connector_can_intercept_fail"
		}
		scout.Report(c, action, entries...)
	}()
	err = s.WithSession(c, func(c context.Context, session userd.Session) error {
		_, result = session.CanIntercept(c, ir)
		if result == nil {
			result = &rpc.InterceptResult{Error: common.InterceptError_UNSPECIFIED}
		}
		entries, ok = s.scoutInterceptEntries(c, ir.GetSpec(), result)
		return nil
	})
	return
}

func (s *service) CreateIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	var entries []scout.Entry
	ok := false
	defer func() {
		var action string
		if ok {
			action = "connector_create_intercept_success"
		} else {
			action = "connector_create_intercept_fail"
		}
		scout.Report(c, action, entries...)
	}()
	err = s.WithSession(c, func(c context.Context, session userd.Session) error {
		result = session.AddIntercept(c, ir)
		entries, ok = s.scoutInterceptEntries(c, ir.GetSpec(), result)
		return nil
	})
	return
}

func (s *service) RemoveIntercept(c context.Context, rr *manager.RemoveInterceptRequest2) (result *rpc.InterceptResult, err error) {
	var spec *manager.InterceptSpec
	var entries []scout.Entry
	ok := false
	defer func() {
		var action string
		if ok {
			action = "connector_remove_intercept_success"
		} else {
			action = "connector_remove_intercept_fail"
		}
		scout.Report(c, action, entries...)
	}()
	err = s.WithSession(c, func(c context.Context, session userd.Session) error {
		result = &rpc.InterceptResult{}
		spec = session.GetInterceptSpec(rr.Name)
		if spec != nil {
			result.ServiceUid = spec.ServiceUid
			result.WorkloadKind = spec.WorkloadKind
		}
		if err := session.RemoveIntercept(c, rr.Name); err != nil {
			if status.Code(err) == codes.NotFound {
				result.Error = common.InterceptError_NOT_FOUND
				result.ErrorText = rr.Name
				result.ErrorCategory = int32(errcat.User)
			} else {
				result.Error = common.InterceptError_TRAFFIC_MANAGER_ERROR
				result.ErrorText = err.Error()
				result.ErrorCategory = int32(errcat.Unknown)
			}
		}
		entries, ok = s.scoutInterceptEntries(c, spec, result)
		return nil
	})
	return result, err
}

func (s *service) UpdateIntercept(c context.Context, rr *manager.UpdateInterceptRequest) (result *manager.InterceptInfo, err error) {
	err = s.WithSession(c, func(c context.Context, session userd.Session) error {
		result, err = session.ManagerClient().UpdateIntercept(c, rr)
		return err
	})
	return
}

func (s *service) AddInterceptor(ctx context.Context, interceptor *rpc.Interceptor) (*empty.Empty, error) {
	return &empty.Empty{}, s.WithSession(ctx, func(c context.Context, session userd.Session) error {
		return session.AddInterceptor(c, interceptor.InterceptId, interceptor)
	})
}

func (s *service) RemoveInterceptor(ctx context.Context, interceptor *rpc.Interceptor) (*empty.Empty, error) {
	return &empty.Empty{}, s.WithSession(ctx, func(_ context.Context, session userd.Session) error {
		return session.RemoveInterceptor(interceptor.InterceptId)
	})
}

func (s *service) List(c context.Context, lr *rpc.ListRequest) (result *rpc.WorkloadInfoSnapshot, err error) {
	err = s.WithSession(c, func(c context.Context, session userd.Session) error {
		result, err = session.WorkloadInfoSnapshot(c, []string{lr.Namespace}, lr.Filter)
		return err
	})
	return
}

func (s *service) GetKnownWorkloadKinds(ctx context.Context, _ *empty.Empty) (result *manager.KnownWorkloadKinds, err error) {
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
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
	var sessionCtx context.Context
	var session userd.Session

	err := s.WithSession(stream.Context(), func(c context.Context, s userd.Session) error {
		session, sessionCtx = s, c
		return nil
	})
	if err != nil {
		return nil
	}

	return session.WatchWorkloads(sessionCtx, wr, stream)
}

func (s *service) Uninstall(c context.Context, ur *rpc.UninstallRequest) (result *common.Result, err error) {
	err = s.WithSession(c, func(c context.Context, session userd.Session) error {
		result, err = session.Uninstall(c, ur)
		return err
	})
	return
}

func (s *service) GetConfig(ctx context.Context, _ *empty.Empty) (cfg *rpc.ClientConfig, err error) {
	err = s.WithSession(ctx, func(c context.Context, session userd.Session) error {
		sc, err := session.GetConfig(ctx)
		if err != nil {
			return err
		}
		data, err := client.MarshalJSON(sc)
		if err != nil {
			return status.Error(codes.Internal, err.Error())
		}
		cfg = &rpc.ClientConfig{Json: data}
		return nil
	})
	return
}

func (s *service) GatherLogs(ctx context.Context, request *rpc.LogsRequest) (result *rpc.LogsResponse, err error) {
	err = s.WithSession(ctx, func(c context.Context, session userd.Session) error {
		result, err = session.GatherLogs(c, request)
		return err
	})
	return
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
		if err = logging.SetAndStoreTimedLevel(ctx, s.timedLogLevel, request.LogLevel, duration, userd.ProcessName); err != nil {
			err = status.Error(codes.Internal, err.Error())
		} else if !s.rootSessionInProc {
			err = s.withRootDaemon(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
				_, err := rd.SetLogLevel(ctx, mrq)
				return err
			})
		}
	}
	setRemote := func() {
		err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
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
	s.cancelSession()
	s.quit()
	_ = s.withRootDaemon(context.WithoutCancel(ctx), func(ctx context.Context, rd daemon.DaemonClient) error {
		dlog.Debug(ctx, "Telling root daemon to Quit")
		_, err := rd.Quit(ctx, ex)
		return err
	})
	return ex, nil
}

func (s *service) RemoteMountAvailability(ctx context.Context, _ *empty.Empty) (*common.Result, error) {
	if proc.RunningInContainer() {
		// We mount using docker volumes and the telemount driver plugin.
		return errcat.ToResult(nil), nil
	}
	if client.GetConfig(ctx).Intercept().UseFtp {
		return errcat.ToResult(s.FuseFTPError()), nil
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
		return errcat.ToResult(errors.New("sshfs is not installed on your local machine")), nil
	}

	// OSXFUSE changed to macFUSE, and we've noticed that older versions of OSXFUSE
	// can cause browsers to hang + kernel crashes, so we add an error to prevent
	// our users from running into this problem.
	// OSXFUSE isn't included in the output of sshfs -V in versions of 4.0.0 so
	// we check for that as a proxy for if they have the right version or not.
	if bytes.Contains(out, []byte("OSXFUSE")) {
		return errcat.ToResult(errors.New(`macFUSE 4.0.5 or higher is required on your local machine`)), nil
	}
	return errcat.ToResult(nil), nil
}

func (s *service) GetNamespaces(ctx context.Context, req *rpc.GetNamespacesRequest) (*rpc.GetNamespacesResponse, error) {
	var resp rpc.GetNamespacesResponse
	err := s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
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
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		vi = &common.VersionInfo{Name: session.ManagerName(), Version: "v" + session.ManagerVersion().String()}
		return nil
	})
	return
}

func (s *service) RootDaemonVersion(ctx context.Context, empty *empty.Empty) (vi *common.VersionInfo, err error) {
	err = s.withRootDaemon(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
		vi, err = rd.Version(ctx, empty)
		return err
	})
	return vi, err
}

func (s *service) AgentImageFQN(ctx context.Context, empty *empty.Empty) (fqn *manager.AgentImageFQN, err error) {
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		fqn, err = session.ManagerClient().GetAgentImageFQN(ctx, empty)
		return err
	})
	return fqn, err
}

func (s *service) GetAgentConfig(ctx context.Context, request *manager.AgentConfigRequest) (rsp *manager.AgentConfigResponse, err error) {
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		request.Session = session.SessionInfo()
		rsp, err = session.ManagerClient().GetAgentConfig(ctx, request)
		return err
	})
	return rsp, err
}

func (s *service) GetClusterSubnets(ctx context.Context, _ *empty.Empty) (cs *rpc.ClusterSubnets, err error) {
	podSubnets := []*manager.IPNet{}
	svcSubnets := []*manager.IPNet{}
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
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
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		ii = session.GetInterceptInfo(request.Name)
		if ii == nil {
			return status.Errorf(codes.NotFound, "found no intercept named %s", request.Name)
		}
		return nil
	})
	return ii, err
}

func (s *service) SetDNSExcludes(ctx context.Context, req *daemon.SetDNSExcludesRequest) (*empty.Empty, error) {
	err := s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		_, err := session.RootDaemon().SetDNSExcludes(ctx, req)
		return err
	})
	return &empty.Empty{}, err
}

func (s *service) SetDNSMappings(ctx context.Context, req *daemon.SetDNSMappingsRequest) (*empty.Empty, error) {
	err := s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		_, err := session.RootDaemon().SetDNSMappings(ctx, req)
		return err
	})
	return &empty.Empty{}, err
}

func (s *service) Ingest(ctx context.Context, request *rpc.IngestRequest) (response *rpc.IngestInfo, err error) {
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		response, err = session.Ingest(ctx, request)
		return err
	})
	return response, err
}

func (s *service) GetIngest(ctx context.Context, request *rpc.IngestIdentifier) (response *rpc.IngestInfo, err error) {
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		response, err = session.GetIngest(request)
		return err
	})
	return response, err
}

func (s *service) LeaveIngest(ctx context.Context, request *rpc.IngestIdentifier) (response *rpc.IngestInfo, err error) {
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		response, err = session.LeaveIngest(ctx, request)
		return err
	})
	return response, err
}

func (s *service) ResolveSyntheticIP(ctx context.Context, request *rpc.ResolveSyntheticRequest) (response *rpc.ResolveSyntheticResponse, err error) {
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
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
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		rsp, err = session.RootDaemon().LookupIP(ctx, request)
		return err
	})
	return rsp, err
}

func (s *service) ResolvePort(ctx context.Context, request *daemon.ResolvePortRequest) (rsp *daemon.ResolvePortResponse, err error) {
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		rsp, err = session.RootDaemon().ResolvePort(ctx, request)
		return err
	})
	return rsp, err
}

func (s *service) RerouteLocalPort(ctx context.Context, request *daemon.ReroutePortRequest) (*empty.Empty, error) {
	err := s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		var ap types.AddrPortProto
		if err := ap.UnmarshalBinary(request.DstHostPort); err != nil {
			return err
		}
		session.RerouteLocalPort(ctx, ap, uint16(request.SrcPort))
		return nil
	})
	return &empty.Empty{}, err
}

func (s *service) RerouteRemotePort(ctx context.Context, request *daemon.ReroutePortRequest) (rsp *empty.Empty, err error) {
	err = s.WithSession(ctx, func(ctx context.Context, session userd.Session) error {
		rsp, err = session.RootDaemon().RerouteRemotePort(ctx, request)
		return err
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
		err = status.Errorf(status.Code(err), "root daemon: %s", err.Error())
	}
	return err
}

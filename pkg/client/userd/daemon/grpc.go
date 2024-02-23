package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/socket"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

func callRecovery(c context.Context, r any, err error) error {
	if perr := derror.PanicToError(r); perr != nil {
		dlog.Errorf(c, "%+v", perr)
		err = perr
	}
	return err
}

type reqNumberKey struct{}

func getReqNumber(ctx context.Context) int64 {
	num := ctx.Value(reqNumberKey{})
	if num == nil {
		return 0
	}
	return num.(int64)
}

func withReqNumber(ctx context.Context, num int64) context.Context {
	return context.WithValue(ctx, reqNumberKey{}, num)
}

func (s *service) callCtx(ctx context.Context, name string) context.Context {
	num := atomic.AddInt64(&s.ucn, 1)
	ctx = withReqNumber(ctx, num)
	return dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s-%d", name, num))
}

func (s *service) logCall(c context.Context, callName string, f func(context.Context)) {
	c = s.callCtx(c, callName)
	dlog.Debug(c, "called")
	defer dlog.Debug(c, "returned")
	f(c)
}

func (s *service) FuseFTPError() error {
	return s.fuseFTPError
}

func (s *service) WithSession(c context.Context, callName string, f func(context.Context, userd.Session) error) (err error) {
	s.logCall(c, callName, func(_ context.Context) {
		if atomic.LoadInt32(&s.sessionQuitting) != 0 {
			err = status.Error(codes.Canceled, "session cancelled")
			return
		}
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		if s.session == nil {
			err = status.Error(codes.Unavailable, "no active session")
			return
		}
		if s.sessionContext.Err() != nil {
			// Session context has been cancelled
			err = status.Error(codes.Canceled, "session cancelled")
			return
		}
		defer func() { err = callRecovery(c, recover(), err) }()
		num := getReqNumber(c)
		ctx := dgroup.WithGoroutineName(s.sessionContext, fmt.Sprintf("/%s-%d", callName, num))
		ctx, span := otel.Tracer("").Start(ctx, callName)
		defer span.End()
		err = f(ctx, s.session)
	})
	return
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
	s.logCall(ctx, "Connect", func(c context.Context) {
		select {
		case <-ctx.Done():
			err = status.Error(codes.Unavailable, ctx.Err().Error())
			return
		case s.connectRequest <- cr:
		}

		select {
		case <-ctx.Done():
			err = status.Error(codes.Unavailable, ctx.Err().Error())
		case result = <-s.connectResponse:
		}
	})
	return result, err
}

func (s *service) Disconnect(ctx context.Context, ex *empty.Empty) (*empty.Empty, error) {
	s.logCall(ctx, "Disconnect", func(ctx context.Context) {
		s.cancelSession()
		_ = s.withRootDaemon(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
			_, err := rd.Disconnect(ctx, ex)
			return err
		})
	})
	return &empty.Empty{}, nil
}

func (s *service) Status(ctx context.Context, ex *empty.Empty) (result *rpc.ConnectInfo, err error) {
	s.logCall(ctx, "Status", func(c context.Context) {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		if s.session == nil {
			result = &rpc.ConnectInfo{Error: rpc.ConnectInfo_DISCONNECTED}
			_ = s.withRootDaemon(c, func(c context.Context, dc daemon.DaemonClient) error {
				result.DaemonStatus, err = dc.Status(c, ex)
				return nil
			})
		} else {
			result = s.session.Status(s.sessionContext)
		}
	})
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
	err = s.WithSession(c, "CanIntercept", func(c context.Context, session userd.Session) error {
		span := trace.SpanFromContext(c)
		tracing.RecordInterceptSpec(span, ir.Spec)
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
	err = s.WithSession(c, "CreateIntercept", func(c context.Context, session userd.Session) error {
		span := trace.SpanFromContext(c)
		tracing.RecordInterceptSpec(span, ir.Spec)
		result = session.AddIntercept(c, ir)
		if result != nil && result.InterceptInfo != nil {
			tracing.RecordInterceptInfo(span, result.InterceptInfo)
		}
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
	err = s.WithSession(c, "RemoveIntercept", func(c context.Context, session userd.Session) error {
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
	err = s.WithSession(c, "UpdateIntercept", func(c context.Context, session userd.Session) error {
		result, err = session.ManagerClient().UpdateIntercept(c, rr)
		return err
	})
	return
}

func (s *service) AddInterceptor(ctx context.Context, interceptor *rpc.Interceptor) (*empty.Empty, error) {
	return &empty.Empty{}, s.WithSession(ctx, "AddInterceptor", func(_ context.Context, session userd.Session) error {
		return session.AddInterceptor(interceptor.InterceptId, interceptor)
	})
}

func (s *service) RemoveInterceptor(ctx context.Context, interceptor *rpc.Interceptor) (*empty.Empty, error) {
	return &empty.Empty{}, s.WithSession(ctx, "RemoveInterceptor", func(_ context.Context, session userd.Session) error {
		return session.RemoveInterceptor(interceptor.InterceptId)
	})
}

func (s *service) List(c context.Context, lr *rpc.ListRequest) (result *rpc.WorkloadInfoSnapshot, err error) {
	err = s.WithSession(c, "List", func(c context.Context, session userd.Session) error {
		result, err = session.WorkloadInfoSnapshot(c, []string{lr.Namespace}, lr.Filter)
		return err
	})
	return
}

func (s *service) WatchWorkloads(wr *rpc.WatchWorkloadsRequest, stream rpc.Connector_WatchWorkloadsServer) error {
	var sessionCtx context.Context
	var session userd.Session

	err := s.WithSession(stream.Context(), "WatchWorkloads", func(c context.Context, s userd.Session) error {
		session, sessionCtx = s, c
		return nil
	})
	if err != nil {
		return nil
	}

	return session.WatchWorkloads(sessionCtx, wr, stream)
}

func (s *service) Uninstall(c context.Context, ur *rpc.UninstallRequest) (result *common.Result, err error) {
	err = s.WithSession(c, "Uninstall", func(c context.Context, session userd.Session) error {
		result, err = session.Uninstall(c, ur)
		return err
	})
	return
}

func (s *service) GetConfig(ctx context.Context, empty *empty.Empty) (cfg *rpc.ClientConfig, err error) {
	err = s.WithSession(ctx, "GetConfig", func(c context.Context, session userd.Session) error {
		sc, err := session.GetConfig(ctx)
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
	return
}

func (s *service) GatherLogs(ctx context.Context, request *rpc.LogsRequest) (result *rpc.LogsResponse, err error) {
	err = s.WithSession(ctx, "GatherLogs", func(c context.Context, session userd.Session) error {
		result, err = session.GatherLogs(c, request)
		return err
	})
	return
}

func (s *service) SetLogLevel(ctx context.Context, request *rpc.LogLevelRequest) (result *empty.Empty, err error) {
	s.logCall(ctx, "SetLogLevel", func(c context.Context) {
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
			err = s.WithSession(ctx, "SetLogLevel", func(ctx context.Context, session userd.Session) error {
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
	})
	return &empty.Empty{}, err
}

func (s *service) Quit(ctx context.Context, ex *empty.Empty) (*empty.Empty, error) {
	s.logCall(ctx, "Quit", func(c context.Context) {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		s.cancelSessionReadLocked()
		s.quit()
		_ = s.withRootDaemon(ctx, func(ctx context.Context, rd daemon.DaemonClient) error {
			_, err := rd.Quit(ctx, ex)
			return err
		})
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
	var cmd *dexec.Cmd
	if runtime.GOOS == "windows" {
		cmd = proc.CommandContext(ctx, "sshfs-win", "cmd", "-V")
	} else {
		cmd = proc.CommandContext(ctx, "sshfs", "-V")
	}
	cmd.DisableLogging = true
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
	err := s.WithSession(ctx, "GetNamespaces", func(ctx context.Context, session userd.Session) error {
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

func (s *service) GatherTraces(ctx context.Context, request *rpc.TracesRequest) (result *common.Result, err error) {
	err = s.WithSession(ctx, "GatherTraces", func(ctx context.Context, session userd.Session) error {
		result = session.GatherTraces(ctx, request)
		return nil
	})
	return
}

func (s *service) TrafficManagerVersion(ctx context.Context, _ *empty.Empty) (vi *common.VersionInfo, err error) {
	err = s.WithSession(ctx, "TrafficManagerVersion", func(ctx context.Context, session userd.Session) error {
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

func (s *service) AgentImageFQN(ctx context.Context, empty *emptypb.Empty) (fqn *manager.AgentImageFQN, err error) {
	err = s.WithSession(ctx, "AgentImageFQN", func(ctx context.Context, session userd.Session) error {
		fqn, err = session.ManagerClient().GetAgentImageFQN(ctx, empty)
		return err
	})
	return fqn, err
}

func (s *service) GetClusterSubnets(ctx context.Context, _ *empty.Empty) (cs *rpc.ClusterSubnets, err error) {
	podSubnets := []*manager.IPNet{}
	svcSubnets := []*manager.IPNet{}
	err = s.WithSession(ctx, "GetClusterSubnets", func(ctx context.Context, session userd.Session) error {
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
			if mgrInfo.ServiceSubnet != nil {
				svcSubnets = append(svcSubnets, mgrInfo.ServiceSubnet)
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
	err = s.WithSession(ctx, "GetIntercept", func(ctx context.Context, session userd.Session) error {
		ii = session.GetInterceptInfo(request.Name)
		if ii == nil {
			return status.Errorf(codes.NotFound, "found no intercept named %s", request.Name)
		}
		return nil
	})
	return ii, err
}

func (s *service) SetDNSExcludes(ctx context.Context, req *daemon.SetDNSExcludesRequest) (*emptypb.Empty, error) {
	err := s.WithSession(ctx, "SetDNSExcludes", func(ctx context.Context, session userd.Session) error {
		_, err := session.RootDaemon().SetDNSExcludes(ctx, req)
		return err
	})
	return &empty.Empty{}, err
}

func (s *service) SetDNSMappings(ctx context.Context, req *daemon.SetDNSMappingsRequest) (*emptypb.Empty, error) {
	err := s.WithSession(ctx, "SetDNSMappings", func(ctx context.Context, session userd.Session) error {
		_, err := session.RootDaemon().SetDNSMappings(ctx, req)
		return err
	})
	return &empty.Empty{}, err
}

func (s *service) withRootDaemon(ctx context.Context, f func(ctx context.Context, daemonClient daemon.DaemonClient) error) error {
	if s.rootSessionInProc {
		return status.Error(codes.Unavailable, "root daemon is embedded")
	}
	conn, err := socket.Dial(ctx, socket.RootDaemonPath(ctx))
	if err == nil {
		defer conn.Close()
		err = f(ctx, daemon.NewDaemonClient(conn))
	}
	if err != nil {
		err = status.Errorf(status.Code(err), "root daemon: %s", err.Error())
	}
	return err
}

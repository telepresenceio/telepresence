package userd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	grpcCodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/rpc/v2/userdaemon"
	"github.com/telepresenceio/telepresence/v2/pkg/a8rcloud"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/commands"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
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

func (s *Service) callCtx(ctx context.Context, name string) context.Context {
	num := atomic.AddInt64(&s.ucn, 1)
	ctx = withReqNumber(ctx, num)
	return dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s-%d", name, num))
}

func (s *Service) logCall(c context.Context, callName string, f func(context.Context)) {
	c = s.callCtx(c, callName)
	dlog.Debug(c, "called")
	defer dlog.Debug(c, "returned")
	f(c)
}

func (s *Service) withSession(c context.Context, callName string, f func(context.Context, trafficmgr.Session) error) (err error) {
	s.logCall(c, callName, func(_ context.Context) {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		if s.session == nil {
			err = status.Error(codes.Unavailable, "no active session")
			return
		}
		defer func() { err = callRecovery(c, recover(), err) }()
		num := getReqNumber(c)
		ctx := dgroup.WithGoroutineName(s.sessionContext, fmt.Sprintf("/%s-%d", callName, num))
		err = f(ctx, s.session)
	})
	return
}

func (s *Service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	executable, err := client.Executable()
	if err != nil {
		return &common.VersionInfo{}, err
	}
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
		Executable: executable,
	}, nil
}

func (s *Service) Connect(ctx context.Context, cr *rpc.ConnectRequest) (result *rpc.ConnectInfo, err error) {
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

func (s *Service) Disconnect(c context.Context, _ *empty.Empty) (*empty.Empty, error) {
	s.logCall(c, "Disconnect", func(c context.Context) {
		s.cancelSession()
	})
	return &empty.Empty{}, nil
}

func (s *Service) Status(c context.Context, _ *empty.Empty) (result *rpc.ConnectInfo, err error) {
	s.logCall(c, "Status", func(c context.Context) {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		if s.session == nil {
			result = &rpc.ConnectInfo{Error: rpc.ConnectInfo_DISCONNECTED}
		} else {
			result = s.session.Status(s.sessionContext)
		}
	})
	return
}

// isMultiPortIntercept checks if the intercept is one of several active intercepts on the same workload.
// If it is, then the first returned value will be true and the second will indicate if those intercepts are
// on different services. Otherwise, this function returns false, false
func (s *Service) isMultiPortIntercept(spec *manager.InterceptSpec) (multiPort, multiService bool) {
	s.sessionLock.RLock()
	defer s.sessionLock.RUnlock()
	if s.session == nil {
		return false, false
	}
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

func (s *Service) scoutInterceptEntries(spec *manager.InterceptSpec, result *rpc.InterceptResult, err error) ([]scout.Entry, bool) {
	// The scout belongs to the session and can only contain session specific meta-data
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
	var msg string
	if result != nil {
		entries = append(entries, scout.Entry{Key: "workload_kind", Value: result.WorkloadKind})
		if result.ServiceProps != nil {
			entries = append(entries, scout.Entry{Key: "service_uid", Value: result.ServiceProps.ServiceUid})
		}
		if result.Error != common.InterceptError_UNSPECIFIED {
			msg = result.Error.String()
		}
	}
	if err != nil && msg == "" {
		msg = err.Error()
	}
	if msg != "" {
		entries = append(entries, scout.Entry{Key: "error", Value: msg})
		return entries, false
	}
	return entries, true
}

func (s *Service) CanIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	defer func() {
		entries, ok := s.scoutInterceptEntries(ir.GetSpec(), result, err)
		var action string
		if ok {
			action = "connector_can_intercept_success"
		} else {
			action = "connector_can_intercept_fail"
		}
		s.scout.Report(c, action, entries...)
	}()
	err = s.withSession(c, "CanIntercept", func(c context.Context, session trafficmgr.Session) error {
		_, result = session.CanIntercept(c, ir)
		if result == nil {
			result = &rpc.InterceptResult{Error: common.InterceptError_UNSPECIFIED}
		}
		return err
	})
	return
}

func (s *Service) CreateIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	defer func() {
		entries, ok := s.scoutInterceptEntries(ir.GetSpec(), result, err)
		var action string
		if ok {
			action = "connector_create_intercept_success"
		} else {
			action = "connector_create_intercept_fail"
		}
		s.scout.Report(c, action, entries...)
	}()
	err = s.withSession(c, "CreateIntercept", func(c context.Context, session trafficmgr.Session) error {
		result, err = session.AddIntercept(c, ir)
		return err
	})
	return
}

func (s *Service) RemoveIntercept(c context.Context, rr *manager.RemoveInterceptRequest2) (result *rpc.InterceptResult, err error) {
	var spec *manager.InterceptSpec
	defer func() {
		entries, ok := s.scoutInterceptEntries(spec, result, err)
		var action string
		if ok {
			action = "connector_remove_intercept_success"
		} else {
			action = "connector_remove_intercept_fail"
		}
		s.scout.Report(c, action, entries...)
	}()
	err = s.withSession(c, "RemoveIntercept", func(c context.Context, session trafficmgr.Session) error {
		result = &rpc.InterceptResult{}
		spec = session.GetInterceptSpec(rr.Name)
		if spec != nil {
			result.ServiceUid = spec.ServiceUid
			result.WorkloadKind = spec.WorkloadKind
		}
		if err := session.RemoveIntercept(c, rr.Name); err != nil {
			if grpcStatus.Code(err) == grpcCodes.NotFound {
				result.Error = common.InterceptError_NOT_FOUND
				result.ErrorText = rr.Name
				result.ErrorCategory = int32(errcat.User)
			} else {
				result.Error = common.InterceptError_TRAFFIC_MANAGER_ERROR
				result.ErrorText = err.Error()
				result.ErrorCategory = int32(errcat.Unknown)
			}
		}
		return nil
	})
	return result, err
}

func (s *Service) AddInterceptor(ctx context.Context, interceptor *rpc.Interceptor) (*empty.Empty, error) {
	return &empty.Empty{}, s.withSession(ctx, "AddInterceptor", func(_ context.Context, session trafficmgr.Session) error {
		return session.AddInterceptor(interceptor.InterceptId, int(interceptor.Pid))
	})
}

func (s *Service) RemoveInterceptor(ctx context.Context, interceptor *rpc.Interceptor) (*empty.Empty, error) {
	return &empty.Empty{}, s.withSession(ctx, "RemoveInterceptor", func(_ context.Context, session trafficmgr.Session) error {
		return session.RemoveInterceptor(interceptor.InterceptId)
	})
}

func (s *Service) List(c context.Context, lr *rpc.ListRequest) (result *rpc.WorkloadInfoSnapshot, err error) {
	err = s.withSession(c, "List", func(c context.Context, session trafficmgr.Session) error {
		result, err = session.WorkloadInfoSnapshot(c, []string{lr.Namespace}, lr.Filter, true)
		return err
	})
	return
}

func (s *Service) WatchWorkloads(wr *rpc.WatchWorkloadsRequest, server rpc.Connector_WatchWorkloadsServer) error {
	return s.withSession(server.Context(), "WatchWorkloads", func(c context.Context, session trafficmgr.Session) error {
		return session.WatchWorkloads(c, wr, server)
	})
}

func (s *Service) Uninstall(c context.Context, ur *rpc.UninstallRequest) (result *rpc.UninstallResult, err error) {
	err = s.withSession(c, "Uninstall", func(c context.Context, session trafficmgr.Session) error {
		result, err = session.Uninstall(c, ur)
		return err
	})
	return
}

func (s *Service) UserNotifications(_ *empty.Empty, stream rpc.Connector_UserNotificationsServer) (err error) {
	s.logCall(stream.Context(), "UserNotifications", func(c context.Context) {
		for msg := range s.userNotifications(c) {
			if err = stream.Send(&rpc.Notification{Message: msg}); err != nil {
				return
			}
		}
	})
	return nil
}

func (s *Service) Login(ctx context.Context, req *rpc.LoginRequest) (result *rpc.LoginResult, err error) {
	s.logCall(ctx, "Login", func(c context.Context) {
		defer func() {
			if err == nil && result.Code == rpc.LoginResult_NEW_LOGIN_SUCCEEDED {
				s.sessionLock.RLock()
				defer s.sessionLock.RUnlock()
				if s.session != nil {
					dlog.Debug(ctx, "Calling remain with new api key")
					err := s.session.RemainWithToken(ctx)
					if err != nil {
						dlog.Warnf(ctx, "Failed to call remain after login: %v", err)
					}
				}
			}
		}()
		if apikey := req.GetApiKey(); apikey != "" {
			var newLogin bool
			if newLogin, err = s.loginExecutor.LoginAPIKey(ctx, apikey); err != nil {
				if errors.Is(err, os.ErrPermission) {
					err = grpcStatus.Error(grpcCodes.PermissionDenied, err.Error())
				}
				return
			}
			dlog.Infof(ctx, "LoginAPIKey => %t", newLogin)
			if newLogin {
				result = &rpc.LoginResult{Code: rpc.LoginResult_NEW_LOGIN_SUCCEEDED}
			} else {
				result = &rpc.LoginResult{Code: rpc.LoginResult_OLD_LOGIN_REUSED}
			}
			return
		}

		// We should refresh here because the user is explicitly logging in so
		// even if we have cache'd user info, if they are unable to get new
		// user info, then it should trigger the login function
		if _, err := s.loginExecutor.GetUserInfo(ctx, true); err == nil {
			result = &rpc.LoginResult{Code: rpc.LoginResult_OLD_LOGIN_REUSED}
		} else if err = s.loginExecutor.Login(ctx); err == nil {
			result = &rpc.LoginResult{Code: rpc.LoginResult_NEW_LOGIN_SUCCEEDED}
		}
	})
	return result, err
}

func (s *Service) Logout(ctx context.Context, _ *empty.Empty) (result *empty.Empty, err error) {
	s.logCall(ctx, "Logout", func(c context.Context) {
		if err = s.loginExecutor.Logout(ctx); err != nil {
			if errors.Is(err, auth.ErrNotLoggedIn) {
				err = grpcStatus.Error(grpcCodes.NotFound, err.Error())
			}
		} else {
			result = &empty.Empty{}
		}
	})
	return
}

func (s *Service) GetCloudUserInfo(ctx context.Context, req *rpc.UserInfoRequest) (result *rpc.UserInfo, err error) {
	s.logCall(ctx, "GetCloudUserInfo", func(c context.Context) {
		result, err = auth.GetCloudUserInfo(ctx, s.loginExecutor, req.GetRefresh(), req.GetAutoLogin())
	})
	return
}

func (s *Service) GetCloudAPIKey(ctx context.Context, req *rpc.KeyRequest) (result *rpc.KeyData, err error) {
	s.logCall(ctx, "GetCloudAPIKey", func(c context.Context) {
		var key string
		if key, err = auth.GetCloudAPIKey(ctx, s.loginExecutor, req.GetDescription(), req.GetAutoLogin()); err == nil {
			result = &rpc.KeyData{ApiKey: key}
		}
	})
	return
}

func (s *Service) GetCloudLicense(ctx context.Context, req *rpc.LicenseRequest) (result *rpc.LicenseData, err error) {
	s.logCall(ctx, "GetCloudLicense", func(c context.Context) {
		var license, hostDomain string
		if license, hostDomain, err = s.loginExecutor.GetLicense(ctx, req.GetId()); err != nil {
			// login is required to get the license from system a so
			// we try to login before retrying the request
			if _err := s.loginExecutor.Login(ctx); _err == nil {
				license, hostDomain, err = s.loginExecutor.GetLicense(ctx, req.GetId())
			}
		}
		if err == nil {
			result = &rpc.LicenseData{License: license, HostDomain: hostDomain}
		}
	})
	return
}

func (s *Service) GetIngressInfos(c context.Context, _ *empty.Empty) (result *rpc.IngressInfos, err error) {
	err = s.withSession(c, "GetIngressInfos", func(c context.Context, session trafficmgr.Session) error {
		var iis []*manager.IngressInfo
		if iis, err = session.IngressInfos(c); err == nil {
			result = &rpc.IngressInfos{IngressInfos: iis}
		}
		return err
	})
	return
}

func (s *Service) GatherLogs(ctx context.Context, request *rpc.LogsRequest) (result *rpc.LogsResponse, err error) {
	err = s.withSession(ctx, "GatherLogs", func(c context.Context, session trafficmgr.Session) error {
		result, err = session.GatherLogs(c, request)
		return err
	})
	return
}

func (s *Service) SetLogLevel(ctx context.Context, request *manager.LogLevelRequest) (result *empty.Empty, err error) {
	s.logCall(ctx, "SetLogLevel", func(c context.Context) {
		duration := time.Duration(0)
		if request.Duration != nil {
			duration = request.Duration.AsDuration()
		}
		if err = logging.SetAndStoreTimedLevel(ctx, s.timedLogLevel, request.LogLevel, duration, s.procName); err != nil {
			err = grpcStatus.Error(grpcCodes.Internal, err.Error())
		} else {
			result = &empty.Empty{}
		}
	})
	return
}

func (s *Service) Quit(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	s.logCall(ctx, "Quit", func(c context.Context) {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		s.cancelSessionReadLocked()
		s.quit()
	})
	return &empty.Empty{}, nil
}

func (s *Service) ListCommands(ctx context.Context, _ *empty.Empty) (groups *rpc.CommandGroups, err error) {
	s.logCall(ctx, "ListCommands", func(ctx context.Context) {
		groups, err = cliutil.CommandsToRPC(s.getCommands(ctx)), nil
	})
	return
}

func (s *Service) RunCommand(ctx context.Context, req *rpc.RunCommandRequest) (result *rpc.RunCommandResponse, err error) {
	s.logCall(ctx, "RunCommand", func(ctx context.Context) {
		cmd := &cobra.Command{
			Use: "fauxmand",
		}
		cli.AddCommandGroups(cmd, s.getCommands(ctx))
		args := req.GetOsArgs()
		cmd.SetArgs(args)
		cmd, args, err = cmd.Find(args)
		if err != nil {
			return
		}
		cmd.SetArgs(args)
		outW, errW := bytes.NewBuffer([]byte{}), bytes.NewBuffer([]byte{})
		cmd.SetOut(outW)
		cmd.SetErr(errW)

		err = cmd.ParseFlags(args)
		if err != nil {
			return
		}

		if _, ok := cmd.Annotations[commands.CommandRequiresSession]; ok {
			err = s.withSession(ctx, "cmd-"+cmd.Name(), func(ctx context.Context, s trafficmgr.Session) error {
				ctx = commands.WithCwd(ctx, req.GetCwd())
				ctx = commands.WithSession(ctx, s)
				return cmd.ExecuteContext(ctx)
			})
		} else {
			ctx = commands.WithCwd(ctx, req.GetCwd())
			err = cmd.ExecuteContext(ctx)
		}
		if _, ok := cmd.Annotations[commands.CommandRequiresConnectorServer]; ok {
			ctx = commands.WithConnectorServer(ctx, s)
		}

		err = cmd.ExecuteContext(ctx)
		if err != nil {
			return
		}

		result = &rpc.RunCommandResponse{
			Stdout: outW.Bytes(),
			Stderr: errW.Bytes(),
		}
	})
	return result, err
}

func (s *Service) ResolveIngressInfo(ctx context.Context, req *userdaemon.IngressInfoRequest) (resp *userdaemon.IngressInfoResponse, err error) {
	err = s.withSession(ctx, "ResolveIngressInfo", func(ctx context.Context, session trafficmgr.Session) error {
		pool := a8rcloud.GetSystemAPool[*SessionClient](ctx, a8rcloud.UserdConnName)
		systemacli, err := pool.Get(ctx)
		if err != nil {
			return err
		}
		defer func() {
			err := pool.Done(ctx)
			dlog.Warnf(ctx, "Unexpected error tearing down systema connection: %v", err)
		}()
		resp, err = systemacli.ResolveIngressInfo(ctx, req)
		return err
	})
	return
}

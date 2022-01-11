package userd_grpc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

type Callbacks struct {
	userd_auth.LoginExecutor
	Cancel            func()
	Connect           func(c context.Context, cr *rpc.ConnectRequest, dryRun bool) *rpc.ConnectInfo
	UserNotifications func(context.Context) <-chan string
}

type service struct {
	rpc.UnsafeConnectorServer

	callbacks     Callbacks
	procName      string
	timedLogLevel log.TimedLevel
	sharedState   *userd_trafficmgr.State

	ucn int64
}

func NewGRPCService(
	ctx context.Context,
	procName string,
	callbacks Callbacks,
	sharedState *userd_trafficmgr.State,
) (rpc.ConnectorServer, error) {
	s := &service{
		callbacks:     callbacks,
		sharedState:   sharedState,
		procName:      procName,
		timedLogLevel: log.NewTimedLevel(client.GetConfig(ctx).LogLevels.UserDaemon.String(), log.SetLevel),
	}
	if err := logging.LoadTimedLevelFromCache(ctx, s.timedLogLevel, s.procName); err != nil {
		return nil, err
	}
	return s, nil
}

func callRecovery(c context.Context, r interface{}, err error) error {
	perr := derror.PanicToError(r)
	if perr != nil {
		if err == nil {
			err = perr
		} else {
			dlog.Errorf(c, "%+v", perr)
		}
	}
	if err != nil {
		dlog.Errorf(c, "%+v", err)
	}
	return err
}

func (s *service) callCtx(ctx context.Context, name string) context.Context {
	return dgroup.WithGoroutineName(ctx, fmt.Sprintf("/%s-%d", name, atomic.AddInt64(&s.ucn, 1)))
}

func (s *service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (s *service) Connect(c context.Context, cr *rpc.ConnectRequest) (ci *rpc.ConnectInfo, err error) {
	c = s.callCtx(c, "Connect")
	dlog.Debug(c, "called")
	defer func() { err = callRecovery(c, recover(), err) }()
	ci, err = s.callbacks.Connect(c, cr, false), nil
	dlog.Debug(c, "returned")
	return
}

func (s *service) Status(c context.Context, cr *rpc.ConnectRequest) (ci *rpc.ConnectInfo, err error) {
	c = s.callCtx(c, "Status")
	dlog.Debug(c, "called")
	defer func() { err = callRecovery(c, recover(), err) }()
	ci, err = s.callbacks.Connect(c, cr, true), nil
	dlog.Debug(c, "returned")
	return
}

func (s *service) CanIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	c = s.callCtx(c, "CanIntercept")
	dlog.Debug(c, "called")
	defer func() { err = callRecovery(c, recover(), err) }()
	result, mgr := s.sharedState.GetTrafficManagerReadyToIntercept()
	if result != nil {
		dlog.Debug(c, "returned")
		return result, nil
	}
	var wl kates.Object
	if result, wl = mgr.CanIntercept(c, ir); result == nil {
		var kind string
		if wl != nil {
			kind = wl.GetObjectKind().GroupVersionKind().Kind
		}
		result = &rpc.InterceptResult{
			Error:        rpc.InterceptError_UNSPECIFIED,
			WorkloadKind: kind,
		}
	}
	dlog.Debug(c, "returned")
	return
}

func (s *service) CreateIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	c = s.callCtx(c, "CreateIntercept")
	dlog.Debug(c, "called")
	defer func() { err = callRecovery(c, recover(), err) }()
	result, mgr := s.sharedState.GetTrafficManagerReadyToIntercept()
	if result != nil {
		dlog.Debug(c, "returned")
		return result, nil
	}
	result, err = mgr.AddIntercept(c, ir)
	dlog.Debug(c, "returned")
	return
}

func (s *service) RemoveIntercept(c context.Context, rr *manager.RemoveInterceptRequest2) (result *rpc.InterceptResult, err error) {
	c = s.callCtx(c, "RemoveIntercept")
	dlog.Debug(c, "called")
	defer func() { err = callRecovery(c, recover(), err) }()
	result, mgr := s.sharedState.GetTrafficManagerReadyToIntercept()
	if result != nil {
		dlog.Debug(c, "returned")
		return result, nil
	}
	result = &rpc.InterceptResult{}
	if err = mgr.RemoveIntercept(c, rr.Name); err != nil {
		if grpcStatus.Code(err) == grpcCodes.NotFound {
			result.Error = rpc.InterceptError_NOT_FOUND
			result.ErrorText = rr.Name
			result.ErrorCategory = int32(errcat.User)
		} else {
			result.Error = rpc.InterceptError_TRAFFIC_MANAGER_ERROR
			result.ErrorText = err.Error()
			result.ErrorCategory = int32(errcat.Unknown)
		}
	}
	dlog.Debug(c, "returned")
	return result, nil
}

func (s *service) List(c context.Context, lr *rpc.ListRequest) (result *rpc.WorkloadInfoSnapshot, err error) {
	c = s.callCtx(c, "List")
	dlog.Debug(c, "called")
	haveManager := false
	mgr, _ := s.sharedState.GetTrafficManagerBlocking(c)
	if mgr != nil {
		managerClient, _ := mgr.GetClientNonBlocking()
		haveManager = (managerClient != nil)
	}
	if !haveManager {
		dlog.Debug(c, "returned")
		return &rpc.WorkloadInfoSnapshot{}, nil
	}

	result, err = mgr.WorkloadInfoSnapshot(c, lr), nil
	dlog.Debug(c, "returned")
	return
}

func (s *service) Uninstall(c context.Context, ur *rpc.UninstallRequest) (result *rpc.UninstallResult, err error) {
	c = s.callCtx(c, "Uninstall")
	dlog.Debug(c, "called")
	defer func() { err = callRecovery(c, recover(), err) }()
	mgr, err := s.sharedState.GetTrafficManagerBlocking(c)
	if mgr == nil {
		dlog.Debug(c, "returned")
		return nil, err
	}
	result, err = mgr.Uninstall(c, ur)
	dlog.Debug(c, "returned")
	return
}

func (s *service) UserNotifications(_ *empty.Empty, stream rpc.Connector_UserNotificationsServer) error {
	ctx := s.callCtx(stream.Context(), "UserNotifications")
	dlog.Debug(ctx, "called")
	for msg := range s.callbacks.UserNotifications(ctx) {
		if err := stream.Send(&rpc.Notification{Message: msg}); err != nil {
			return err
		}
	}
	dlog.Debug(ctx, "returned")
	return nil
}

func (s *service) Login(ctx context.Context, req *rpc.LoginRequest) (*rpc.LoginResult, error) {
	ctx = s.callCtx(ctx, "Login")
	dlog.Debug(ctx, "called")
	if apikey := req.GetApiKey(); apikey != "" {
		newLogin, err := s.callbacks.LoginAPIKey(ctx, apikey)
		dlog.Infof(ctx, "LoginAPIKey => (%v, %v)", newLogin, err)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				err = grpcStatus.Error(grpcCodes.PermissionDenied, err.Error())
			}
			dlog.Debug(ctx, "returned")
			return nil, err
		}
		if !newLogin {
			dlog.Debug(ctx, "returned")
			return &rpc.LoginResult{Code: rpc.LoginResult_OLD_LOGIN_REUSED}, nil
		}
	} else {
		// We should refresh here because the user is explicitly logging in so
		// even if we have cache'd user info, if they are unable to get new
		// user info, then it should trigger the login function
		if _, err := s.callbacks.GetUserInfo(ctx, true); err == nil {
			dlog.Debug(ctx, "returned")
			return &rpc.LoginResult{Code: rpc.LoginResult_OLD_LOGIN_REUSED}, nil
		}
		if err := s.callbacks.Login(ctx); err != nil {
			dlog.Debug(ctx, "returned")
			return nil, err
		}
	}
	dlog.Debug(ctx, "returned")
	return &rpc.LoginResult{Code: rpc.LoginResult_NEW_LOGIN_SUCCEEDED}, nil
}

func (s *service) Logout(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	ctx = s.callCtx(ctx, "Logout")
	dlog.Debug(ctx, "called")
	if err := s.callbacks.Logout(ctx); err != nil {
		if errors.Is(err, userd_auth.ErrNotLoggedIn) {
			err = grpcStatus.Error(grpcCodes.NotFound, err.Error())
		}
		dlog.Debug(ctx, "returned")
		return nil, err
	}
	dlog.Debug(ctx, "returned")
	return &empty.Empty{}, nil
}

func (s *service) GetCloudUserInfo(ctx context.Context, req *rpc.UserInfoRequest) (*rpc.UserInfo, error) {
	ctx = s.callCtx(ctx, "GetCloudUserInfo")
	dlog.Debug(ctx, "called")
	info, err := s.callbacks.GetCloudUserInfo(ctx, req.GetRefresh(), req.GetAutoLogin())
	if err != nil {
		dlog.Debug(ctx, "returned")
		return nil, err
	}
	dlog.Debug(ctx, "returned")
	return info, nil
}

func (s *service) GetCloudAPIKey(ctx context.Context, req *rpc.KeyRequest) (*rpc.KeyData, error) {
	ctx = s.callCtx(ctx, "GetCloudAPIKey")
	dlog.Debug(ctx, "called")
	key, err := s.callbacks.GetCloudAPIKey(ctx, req.GetDescription(), req.GetAutoLogin())
	if err != nil {
		dlog.Debug(ctx, "returned")
		return nil, err
	}
	dlog.Debug(ctx, "returned")
	return &rpc.KeyData{ApiKey: key}, nil
}

func (s *service) GetCloudLicense(ctx context.Context, req *rpc.LicenseRequest) (*rpc.LicenseData, error) {
	ctx = s.callCtx(ctx, "GetCloudLicense")
	dlog.Debug(ctx, "called")
	license, hostDomain, err := s.callbacks.GetLicense(ctx, req.GetId())
	// login is required to get the license from system a so
	// we try to login before retrying the request
	if err != nil {
		if _err := s.callbacks.Login(ctx); _err == nil {
			license, hostDomain, err = s.callbacks.GetLicense(ctx, req.GetId())
		}
	}
	if err != nil {
		dlog.Debug(ctx, "returned")
		return nil, err
	}
	dlog.Debug(ctx, "returned")
	return &rpc.LicenseData{License: license, HostDomain: hostDomain}, nil
}

func (s *service) SetLogLevel(ctx context.Context, request *manager.LogLevelRequest) (*empty.Empty, error) {
	ctx = s.callCtx(ctx, "SetLogLevel")
	dlog.Debug(ctx, "called")
	duration := time.Duration(0)
	if request.Duration != nil {
		duration = request.Duration.AsDuration()
	}
	err := logging.SetAndStoreTimedLevel(ctx, s.timedLogLevel, request.LogLevel, duration, s.procName)
	if err != nil {
		dlog.Debug(ctx, "returned")
		return nil, grpcStatus.Error(grpcCodes.Internal, err.Error())
	}
	dlog.Debug(ctx, "returned")
	return &empty.Empty{}, nil
}

func (s *service) Quit(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	ctx = s.callCtx(ctx, "Quit")
	dlog.Debug(ctx, "called")
	s.callbacks.Cancel()
	dlog.Debug(ctx, "returned")
	return &empty.Empty{}, nil
}

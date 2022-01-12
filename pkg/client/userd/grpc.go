package userd

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
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
)

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

func (s *service) logCall(c context.Context, callName string, f func(context.Context)) {
	c = s.callCtx(c, callName)
	dlog.Debug(c, "called")
	defer dlog.Debug(c, "returned")
	f(c)
}

func (s *service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (s *service) Connect(c context.Context, cr *rpc.ConnectRequest) (ci *rpc.ConnectInfo, err error) {
	s.logCall(c, "Connect", func(c context.Context) {
		defer func() { err = callRecovery(c, recover(), err) }()
		ci, err = s.connect(c, cr, false), nil
	})
	return
}

func (s *service) Status(c context.Context, cr *rpc.ConnectRequest) (ci *rpc.ConnectInfo, err error) {
	s.logCall(c, "Status", func(c context.Context) {
		defer func() { err = callRecovery(c, recover(), err) }()
		ci, err = s.connect(c, cr, true), nil
	})
	return
}

func (s *service) CanIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	s.logCall(c, "CanIntercept", func(c context.Context) {
		defer func() { err = callRecovery(c, recover(), err) }()
		var mgr *trafficmgr.TrafficManager
		if result, mgr = s.sharedState.GetTrafficManagerReadyToIntercept(); result != nil {
			return
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
	})
	return
}

func (s *service) CreateIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	s.logCall(c, "CreateIntercept", func(c context.Context) {
		defer func() { err = callRecovery(c, recover(), err) }()
		var mgr *trafficmgr.TrafficManager
		if result, mgr = s.sharedState.GetTrafficManagerReadyToIntercept(); result != nil {
			return
		}
		result, err = mgr.AddIntercept(c, ir)
	})
	return
}

func (s *service) RemoveIntercept(c context.Context, rr *manager.RemoveInterceptRequest2) (result *rpc.InterceptResult, err error) {
	s.logCall(c, "RemoveIntercept", func(c context.Context) {
		defer func() { err = callRecovery(c, recover(), err) }()
		var mgr *trafficmgr.TrafficManager
		if result, mgr = s.sharedState.GetTrafficManagerReadyToIntercept(); result != nil {
			return
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
	})
	return
}

func (s *service) List(c context.Context, lr *rpc.ListRequest) (result *rpc.WorkloadInfoSnapshot, err error) {
	s.logCall(c, "List", func(c context.Context) {
		defer func() { err = callRecovery(c, recover(), err) }()
		haveManager := false
		var mgr *trafficmgr.TrafficManager
		mgr, _ = s.sharedState.GetTrafficManagerBlocking(c)
		if mgr != nil {
			managerClient, _ := mgr.GetClientNonBlocking()
			haveManager = (managerClient != nil)
		}
		if haveManager {
			result, err = mgr.WorkloadInfoSnapshot(c, lr), nil
		} else {
			result = &rpc.WorkloadInfoSnapshot{}
		}
	})
	return
}

func (s *service) Uninstall(c context.Context, ur *rpc.UninstallRequest) (result *rpc.UninstallResult, err error) {
	s.logCall(c, "Uninstall", func(c context.Context) {
		defer func() { err = callRecovery(c, recover(), err) }()
		var mgr *trafficmgr.TrafficManager
		mgr, err = s.sharedState.GetTrafficManagerBlocking(c)
		if mgr == nil {
			return
		}
		result, err = mgr.Uninstall(c, ur)
	})
	return
}

func (s *service) UserNotifications(_ *empty.Empty, stream rpc.Connector_UserNotificationsServer) (err error) {
	s.logCall(stream.Context(), "UserNotifications", func(c context.Context) {
		for msg := range s.userNotifications(c) {
			if err = stream.Send(&rpc.Notification{Message: msg}); err != nil {
				return
			}
		}
	})
	return nil
}

func (s *service) Login(ctx context.Context, req *rpc.LoginRequest) (result *rpc.LoginResult, err error) {
	s.logCall(ctx, "Login", func(c context.Context) {
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

func (s *service) Logout(ctx context.Context, _ *empty.Empty) (result *empty.Empty, err error) {
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

func (s *service) GetCloudUserInfo(ctx context.Context, req *rpc.UserInfoRequest) (result *rpc.UserInfo, err error) {
	s.logCall(ctx, "GetCloudUserInfo", func(c context.Context) {
		result, err = s.loginExecutor.GetCloudUserInfo(ctx, req.GetRefresh(), req.GetAutoLogin())
	})
	return
}

func (s *service) GetCloudAPIKey(ctx context.Context, req *rpc.KeyRequest) (result *rpc.KeyData, err error) {
	s.logCall(ctx, "GetCloudUserInfo", func(c context.Context) {
		var key string
		if key, err = s.loginExecutor.GetCloudAPIKey(ctx, req.GetDescription(), req.GetAutoLogin()); err == nil {
			result = &rpc.KeyData{ApiKey: key}
		}
	})
	return
}

func (s *service) GetCloudLicense(ctx context.Context, req *rpc.LicenseRequest) (result *rpc.LicenseData, err error) {
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

func (s *service) SetLogLevel(ctx context.Context, request *manager.LogLevelRequest) (result *empty.Empty, err error) {
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

func (s *service) Quit(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	s.logCall(ctx, "Quit", func(c context.Context) {
		s.cancel()
	})
	return &empty.Empty{}, nil
}

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
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/commands"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func callRecovery(r interface{}, err error) error {
	if perr := derror.PanicToError(r); perr != nil {
		err = perr
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

func (s *service) withSession(c context.Context, callName string, f func(context.Context, trafficmgr.Session) error) (err error) {
	s.logCall(c, callName, func(_ context.Context) {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		if s.session == nil {
			err = status.Error(codes.Unavailable, "no active session")
			return
		}
		defer func() { err = callRecovery(recover(), err) }()
		err = f(s.sessionContext, s.session)
	})
	return
}

func (s *service) Version(_ context.Context, _ *empty.Empty) (*common.VersionInfo, error) {
	return &common.VersionInfo{
		ApiVersion: client.APIVersion,
		Version:    client.Version(),
	}, nil
}

func (s *service) Connect(ctx context.Context, cr *rpc.ConnectRequest) (result *rpc.ConnectInfo, err error) {
	s.logCall(ctx, "Connect", func(c context.Context) {
		s.sessionLock.RLock()
		if s.session != nil {
			result = s.session.UpdateStatus(s.sessionContext, cr)
			s.sessionLock.RUnlock()
			return
		}
		s.sessionLock.RUnlock()

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

func (s *service) Disconnect(c context.Context, _ *empty.Empty) (*empty.Empty, error) {
	s.logCall(c, "Disconnect", func(c context.Context) {
		s.sessionLock.RLock()
		defer s.sessionLock.RUnlock()
		if s.session != nil {
			s.sessionCancel()
		}
	})
	return &empty.Empty{}, nil
}

func (s *service) Status(c context.Context, _ *empty.Empty) (result *rpc.ConnectInfo, err error) {
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

func (s *service) CanIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	err = s.withSession(c, "CanIntercept", func(c context.Context, session trafficmgr.Session) error {
		var wl k8sapi.Workload
		if result, wl = session.CanIntercept(c, ir); result == nil {
			var kind string
			if wl != nil {
				kind = wl.GetKind()
			}
			result = &rpc.InterceptResult{
				Error:        rpc.InterceptError_UNSPECIFIED,
				WorkloadKind: kind,
			}
		}
		return nil
	})
	return
}

func (s *service) CreateIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	err = s.withSession(c, "CreateIntercept", func(c context.Context, session trafficmgr.Session) error {
		result, err = session.AddIntercept(c, ir)
		return err
	})
	return
}

func (s *service) RemoveIntercept(c context.Context, rr *manager.RemoveInterceptRequest2) (result *rpc.InterceptResult, err error) {
	err = s.withSession(c, "RemoveIntercept", func(c context.Context, session trafficmgr.Session) error {
		result = &rpc.InterceptResult{}
		if err := session.RemoveIntercept(c, rr.Name); err != nil {
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
		return nil
	})
	return
}

func (s *service) List(c context.Context, lr *rpc.ListRequest) (result *rpc.WorkloadInfoSnapshot, err error) {
	err = s.withSession(c, "List", func(c context.Context, session trafficmgr.Session) error {
		result, err = session.WorkloadInfoSnapshot(c, []string{lr.Namespace}, lr.Filter, true)
		return err
	})
	return
}

func (s *service) Uninstall(c context.Context, ur *rpc.UninstallRequest) (result *rpc.UninstallResult, err error) {
	err = s.withSession(c, "Uninstall", func(c context.Context, session trafficmgr.Session) error {
		result, err = session.Uninstall(c, ur)
		return err
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
	s.logCall(ctx, "GetCloudAPIKey", func(c context.Context) {
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

func (s *service) GetIngressInfos(c context.Context, _ *empty.Empty) (result *rpc.IngressInfos, err error) {
	err = s.withSession(c, "GetIngressInfos", func(c context.Context, session trafficmgr.Session) error {
		var iis []*manager.IngressInfo
		if iis, err = session.IngressInfos(c); err == nil {
			result = &rpc.IngressInfos{IngressInfos: iis}
		}
		return err
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
		s.quit()
	})
	return &empty.Empty{}, nil
}

func (s *service) ListCommands(ctx context.Context, _ *empty.Empty) (*rpc.CommandGroups, error) {
	return cliutil.CommandsToRPC(s.getCommands()), nil
}

func (s *service) RunCommand(ctx context.Context, req *rpc.RunCommandRequest) (*rpc.RunCommandResponse, error) {
	cmd := &cobra.Command{
		Use: "fauxmand",
	}
	cli.AddCommandGroups(cmd, commands.GetCommands())
	cmd.SetArgs(req.GetOsArgs())
	outW, errW := bytes.NewBuffer([]byte{}), bytes.NewBuffer([]byte{})
	cmd.SetOut(outW)
	cmd.SetErr(errW)
	err := cmd.ExecuteContext(ctx)
	if err != nil {
		return nil, err
	}
	return &rpc.RunCommandResponse{
		Stdout: outW.Bytes(),
		Stderr: errW.Bytes(),
	}, nil
}

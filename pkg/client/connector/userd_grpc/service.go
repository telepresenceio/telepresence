package userd_grpc

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dgroup"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/sharedstate"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth"
)

type Callbacks struct {
	InterceptStatus func() (rpc.InterceptError, string)
	Cancel          func()
	Connect         func(c context.Context, cr *rpc.ConnectRequest, dryRun bool) *rpc.ConnectInfo
}

type service struct {
	rpc.UnsafeConnectorServer

	callbacks   Callbacks
	sharedState *sharedstate.State

	ucn int64
}

func NewGRPCService(
	callbacks Callbacks,
	sharedState *sharedstate.State,
) rpc.ConnectorServer {
	return &service{
		callbacks:   callbacks,
		sharedState: sharedState,
	}
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
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.callbacks.Connect(c, cr, false), nil
}

func (s *service) Status(c context.Context, cr *rpc.ConnectRequest) (ci *rpc.ConnectInfo, err error) {
	c = s.callCtx(c, "Status")
	defer func() { err = callRecovery(c, recover(), err) }()
	return s.callbacks.Connect(c, cr, true), nil
}

func (s *service) CreateIntercept(c context.Context, ir *rpc.CreateInterceptRequest) (result *rpc.InterceptResult, err error) {
	ie, is := s.callbacks.InterceptStatus()
	if ie != rpc.InterceptError_UNSPECIFIED {
		return &rpc.InterceptResult{Error: ie, ErrorText: is}, nil
	}
	c = s.callCtx(c, "CreateIntercept")
	defer func() { err = callRecovery(c, recover(), err) }()
	mgr, err := s.sharedState.GetTrafficManagerBlocking(c)
	if mgr == nil {
		return nil, err
	}
	return mgr.AddIntercept(c, ir)
}

func (s *service) RemoveIntercept(c context.Context, rr *manager.RemoveInterceptRequest2) (result *rpc.InterceptResult, err error) {
	ie, is := s.callbacks.InterceptStatus()
	if ie != rpc.InterceptError_UNSPECIFIED {
		return &rpc.InterceptResult{Error: ie, ErrorText: is}, nil
	}
	c = s.callCtx(c, "RemoveIntercept")
	defer func() { err = callRecovery(c, recover(), err) }()
	mgr, err := s.sharedState.GetTrafficManagerBlocking(c)
	if mgr == nil {
		return nil, err
	}
	err = mgr.RemoveIntercept(c, rr.Name)
	return &rpc.InterceptResult{}, err
}

func (s *service) List(ctx context.Context, lr *rpc.ListRequest) (*rpc.WorkloadInfoSnapshot, error) {
	haveManager := false
	manager, _ := s.sharedState.GetTrafficManagerBlocking(ctx)
	if manager != nil {
		managerClient, _ := manager.GetClientNonBlocking()
		haveManager = (managerClient != nil)
	}
	if !haveManager {
		return &rpc.WorkloadInfoSnapshot{}, nil
	}

	return manager.WorkloadInfoSnapshot(ctx, lr), nil
}

func (s *service) Uninstall(c context.Context, ur *rpc.UninstallRequest) (result *rpc.UninstallResult, err error) {
	c = s.callCtx(c, "Uninstall")
	defer func() { err = callRecovery(c, recover(), err) }()
	mgr, err := s.sharedState.GetTrafficManagerBlocking(c)
	if mgr == nil {
		return nil, err
	}
	return mgr.Uninstall(c, ur)
}

func (s *service) UserNotifications(_ *empty.Empty, stream rpc.Connector_UserNotificationsServer) error {
	ctx := s.callCtx(stream.Context(), "UserNotifications")

	for msg := range s.sharedState.UserNotifications.Subscribe(ctx) {
		if err := stream.Send(&rpc.Notification{Message: msg}); err != nil {
			return err
		}
	}

	return nil
}

func (s *service) Login(ctx context.Context, _ *empty.Empty) (*rpc.LoginResult, error) {
	ctx = s.callCtx(ctx, "Login")
	if _, err := s.sharedState.LoginExecutor.GetUserInfo(ctx, false); err == nil {
		return &rpc.LoginResult{Code: rpc.LoginResult_OLD_LOGIN_REUSED}, nil
	}
	if err := s.sharedState.LoginExecutor.Login(ctx); err != nil {
		return nil, err
	}
	return &rpc.LoginResult{Code: rpc.LoginResult_NEW_LOGIN_SUCCEEDED}, nil
}

func (s *service) Logout(ctx context.Context, _ *empty.Empty) (*empty.Empty, error) {
	ctx = s.callCtx(ctx, "Logout")
	if err := s.sharedState.LoginExecutor.Logout(ctx); err != nil {
		if errors.Is(err, userd_auth.ErrNotLoggedIn) {
			err = grpcStatus.Error(grpcCodes.NotFound, err.Error())
		}
		return nil, err
	}
	return &empty.Empty{}, nil
}

func (s *service) GetCloudUserInfo(ctx context.Context, req *rpc.UserInfoRequest) (*rpc.UserInfo, error) {
	ctx = s.callCtx(ctx, "GetCloudUserInfo")
	info, err := s.sharedState.LoginExecutor.GetUserInfo(ctx, req.GetRefresh())
	if req.GetAutoLogin() && err != nil {
		if _err := s.sharedState.LoginExecutor.Login(ctx); _err == nil {
			info, err = s.sharedState.LoginExecutor.GetUserInfo(ctx, req.GetRefresh())
		}
	}
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (s *service) GetCloudAPIKey(ctx context.Context, req *rpc.KeyRequest) (*rpc.KeyData, error) {
	ctx = s.callCtx(ctx, "GetCloudAPIKey")
	key, err := s.sharedState.GetCloudAPIKey(ctx, req.GetDescription(), req.GetAutoLogin())
	if err != nil {
		return nil, err
	}
	return &rpc.KeyData{ApiKey: key}, nil
}

func (s *service) GetCloudLicense(ctx context.Context, req *rpc.LicenseRequest) (*rpc.LicenseData, error) {
	ctx = s.callCtx(ctx, "GetCloudLicense")

	license, hostDomain, err := s.sharedState.LoginExecutor.GetLicense(ctx, req.GetId())
	// login is required to get the license from system a so
	// we try to login before retrying the request
	if err != nil {
		if _err := s.sharedState.LoginExecutor.Login(ctx); _err == nil {
			license, hostDomain, err = s.sharedState.LoginExecutor.GetLicense(ctx, req.GetId())
		}
	}
	if err != nil {
		return nil, err
	}
	return &rpc.LicenseData{License: license, HostDomain: hostDomain}, nil
}

func (s *service) Quit(_ context.Context, _ *empty.Empty) (*empty.Empty, error) {
	s.callbacks.Cancel()
	return &empty.Empty{}, nil
}

package sharedstate

import (
	"context"
	"time"

	"github.com/datawire/ambassador/v2/pkg/kates"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/broadcastqueue"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth/authdata"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/logging"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

// A TrafficManager implementation is essentially the goroutine that handles communication with the
// in-cluster Traffic Manager.  It includes a "Run" method which is what runs in the goroutine, and
// several other methods to communicate with that goroutine.
type TrafficManager interface {
	// Run is the "main" method that runs in a dedicated persistent goroutine.
	Run(context.Context) error

	// GetClientBlocking returns a client object for talking to the manager.  If communication
	// is not yet established, GetClientBlocking blocks until it is (or until the Context is
	// canceled).  Error is non-nil if either there is an error establishing communication or if
	// the context is canceled.
	GetClientBlocking(ctx context.Context) (manager.ManagerClient, error)

	// GetClientNonBlocking is similar to GetClientBlocking, but if communication is not yet
	// established then it immediately returns (nil, nil) rather than blocking; this is the only
	// scenario in which both are nil.
	GetClientNonBlocking() (manager.ManagerClient, error)

	// CanIntercept checks if it is possible to create an intercept for the given request. The intercept can proceed
	// only if the returned rpc.InterceptResult is nil. The returned kates.Object is either nil, indicating a local
	// intercept, or the workload for the intercept.
	CanIntercept(context.Context, *connector.CreateInterceptRequest) (*connector.InterceptResult, kates.Object)

	AddIntercept(context.Context, *connector.CreateInterceptRequest) (*connector.InterceptResult, error)

	RemoveIntercept(context.Context, string) error
	WorkloadInfoSnapshot(context.Context, *connector.ListRequest) *connector.WorkloadInfoSnapshot
	Uninstall(context.Context, *connector.UninstallRequest) (*connector.UninstallResult, error)
	SetStatus(context.Context, *connector.ConnectInfo)
}

type State struct {
	LoginExecutor     userd_auth.LoginExecutor
	UserNotifications broadcastqueue.BroadcastQueue

	clusterFinalized chan struct{}
	cluster          *userd_k8s.Cluster

	trafficMgrFinalized chan struct{}
	trafficMgr          TrafficManager
	procName            string
	timedLogLevel       log.TimedLevel
}

type ROState interface {
	GetClusterBlocking(ctx context.Context) (*userd_k8s.Cluster, error)
	GetClusterNonBlocking() *userd_k8s.Cluster
	GetTrafficManagerNonBlocking() TrafficManager
	GetTrafficManagerBlocking(ctx context.Context) (TrafficManager, error)
	GetTrafficManagerReadyToIntercept() (*connector.InterceptResult, TrafficManager)
	GetCloudUserInfo(ctx context.Context, refresh, autoLogin bool) (*authdata.UserInfo, error)
	GetCloudAPIKey(ctx context.Context, desc string, autoLogin bool) (string, error)
}

func NewState(ctx context.Context, procName string) (*State, error) {
	s := &State{
		//LoginExecutor:     "Caller will initialize this later",
		//UserNotifications: "The zero value is fine",
		clusterFinalized:    make(chan struct{}),
		trafficMgrFinalized: make(chan struct{}),
		procName:            procName,
		timedLogLevel:       log.NewTimedLevel(client.GetConfig(ctx).LogLevels.UserDaemon.String(), log.SetLevel),
	}
	return s, logging.LoadTimedLevelFromCache(ctx, s.timedLogLevel, procName)
}

func (s *State) MaybeSetCluster(cluster *userd_k8s.Cluster) bool {
	select {
	case <-s.clusterFinalized:
		return false
	default:
		s.cluster = cluster
		close(s.clusterFinalized)
		return true
	}
}

func (s *State) GetClusterBlocking(ctx context.Context) (*userd_k8s.Cluster, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.clusterFinalized:
		return s.cluster, nil
	}
}

func (s *State) GetClusterNonBlocking() *userd_k8s.Cluster {
	select {
	case <-s.clusterFinalized:
		return s.cluster
	default:
		return nil
	}
}

func (s *State) MaybeSetTrafficManager(mgr TrafficManager) bool {
	select {
	case <-s.trafficMgrFinalized:
		return false
	default:
		s.trafficMgr = mgr
		close(s.trafficMgrFinalized)
		return true
	}
}

func (s *State) GetTrafficManagerBlocking(ctx context.Context) (TrafficManager, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.trafficMgrFinalized:
		return s.trafficMgr, nil
	}
}

func (s *State) GetTrafficManagerNonBlocking() TrafficManager {
	select {
	case <-s.trafficMgrFinalized:
		return s.trafficMgr
	default:
		return nil
	}
}

func (s *State) GetTrafficManagerReadyToIntercept() (*connector.InterceptResult, TrafficManager) {
	var ie connector.InterceptError
	switch {
	case s.cluster == nil:
		ie = connector.InterceptError_NO_CONNECTION
	case s.trafficMgr == nil:
		ie = connector.InterceptError_NO_TRAFFIC_MANAGER
	default:
		if mgrClient, mgrErr := s.trafficMgr.GetClientNonBlocking(); mgrClient == nil {
			if mgrErr != nil {
				// there was an error connecting
				return &connector.InterceptResult{
					Error:         connector.InterceptError_TRAFFIC_MANAGER_ERROR,
					ErrorText:     mgrErr.Error(),
					ErrorCategory: int32(errcat.GetCategory(mgrErr)),
				}, nil
			}
			// still in the process of connecting but not connected yet
			ie = connector.InterceptError_TRAFFIC_MANAGER_CONNECTING
		} else {
			return nil, s.trafficMgr
		}
	}
	return &connector.InterceptResult{Error: ie}, nil
}

func (s *State) GetCloudUserInfo(ctx context.Context, refresh, autoLogin bool) (*authdata.UserInfo, error) {
	info, err := s.LoginExecutor.GetUserInfo(ctx, refresh)
	if autoLogin && err != nil {
		// Opportunistically log in; if it fails, don't sweat it and discard the error.
		if _err := s.LoginExecutor.Login(ctx); _err == nil {
			info, err = s.LoginExecutor.GetUserInfo(ctx, refresh)
		}
	}
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (s *State) GetCloudAPIKey(ctx context.Context, desc string, autoLogin bool) (string, error) {
	key, err := s.LoginExecutor.GetAPIKey(ctx, desc)
	if autoLogin && err != nil {
		// Opportunistically log in; if it fails, don't sweat it and discard the error.
		if _err := s.LoginExecutor.Login(ctx); _err == nil {
			key, err = s.LoginExecutor.GetAPIKey(ctx, desc)
		}
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

func (s *State) SetLogLevel(ctx context.Context, level string, duration time.Duration) error {
	return logging.SetAndStoreTimedLevel(ctx, s.timedLogLevel, level, duration, s.procName)
}

package sharedstate

import (
	"context"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/internal/broadcastqueue"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_k8s"
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

	AddIntercept(context.Context, *connector.CreateInterceptRequest) (*connector.InterceptResult, error)
	RemoveIntercept(context.Context, string) error
	WorkloadInfoSnapshot(context.Context, *connector.ListRequest) *connector.WorkloadInfoSnapshot
	Uninstall(context.Context, *connector.UninstallRequest) (*connector.UninstallResult, error)
}

type State struct {
	LoginExecutor     userd_auth.LoginExecutor
	UserNotifications broadcastqueue.BroadcastQueue

	clusterFinalized chan struct{}
	cluster          *userd_k8s.Cluster

	trafficMgrFinalized chan struct{}
	trafficMgr          TrafficManager
}

func NewState() *State {
	return &State{
		//LoginExecutor:     "Caller will initialize this later",
		//UserNotifications: "The zero value is fine",
		clusterFinalized:    make(chan struct{}),
		trafficMgrFinalized: make(chan struct{}),
	}
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

func (s *State) GetCloudAPIKey(ctx context.Context, desc string, autoLogin bool) (string, error) {
	key, err := s.LoginExecutor.GetAPIKey(ctx, desc)
	if autoLogin && err != nil {
		if _err := s.LoginExecutor.Login(ctx); _err == nil {
			key, err = s.LoginExecutor.GetAPIKey(ctx, desc)
		}
	}
	if err != nil {
		return "", err
	}
	return key, nil
}

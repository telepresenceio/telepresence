package userd_trafficmgr

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/userd_k8s"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

type State struct {
	clusterFinalized chan struct{}
	cluster          *userd_k8s.Cluster

	trafficMgrFinalized chan struct{}
	trafficMgr          *TrafficManager
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

func (s *State) MaybeSetTrafficManager(mgr *TrafficManager) bool {
	select {
	case <-s.trafficMgrFinalized:
		return false
	default:
		s.trafficMgr = mgr
		close(s.trafficMgrFinalized)
		return true
	}
}

func (s *State) GetTrafficManagerBlocking(ctx context.Context) (*TrafficManager, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.trafficMgrFinalized:
		return s.trafficMgr, nil
	}
}

func (s *State) GetTrafficManagerNonBlocking() *TrafficManager {
	select {
	case <-s.trafficMgrFinalized:
		return s.trafficMgr
	default:
		return nil
	}
}

func (s *State) GetTrafficManagerReadyToIntercept() (*connector.InterceptResult, *TrafficManager) {
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

package connector

import (
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
)

func (s *service) interceptStatus() *rpc.InterceptResult {
	var ie rpc.InterceptError
	mgr := s.sharedState.GetTrafficManagerNonBlocking()
	switch {
	case s.sharedState.GetClusterNonBlocking() == nil:
		ie = rpc.InterceptError_NO_CONNECTION
	case mgr == nil:
		ie = rpc.InterceptError_NO_TRAFFIC_MANAGER
	default:
		if mgrClient, mgrErr := mgr.GetClientNonBlocking(); mgrClient == nil {
			if mgrErr != nil {
				// there was an error connecting
				return &rpc.InterceptResult{
					Error:         rpc.InterceptError_TRAFFIC_MANAGER_ERROR,
					ErrorText:     mgrErr.Error(),
					ErrorCategory: int32(errcat.GetCategory(mgrErr)),
				}
			}
			// still in the process of connecting but not connected yet
			ie = rpc.InterceptError_TRAFFIC_MANAGER_CONNECTING
		} else {
			return nil
		}
	}
	return &rpc.InterceptResult{Error: ie}
}

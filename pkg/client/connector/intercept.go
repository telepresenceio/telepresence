package connector

import (
	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
)

func (s *service) interceptStatus() (rpc.InterceptError, string) {
	ie := rpc.InterceptError_UNSPECIFIED
	msg := ""
	mgr := s.sharedState.GetTrafficManagerNonBlocking()
	switch {
	case s.sharedState.GetClusterNonBlocking() == nil:
		ie = rpc.InterceptError_NO_CONNECTION
	case mgr == nil:
		ie = rpc.InterceptError_NO_TRAFFIC_MANAGER
	default:
		if mgrClient, mgrErr := mgr.GetClientNonBlocking(); mgrClient == nil {
			if mgrErr == nil {
				// still in the process of connectingnot connected yet
				ie = rpc.InterceptError_TRAFFIC_MANAGER_CONNECTING
			} else {
				// there was an error connecting
				ie = rpc.InterceptError_TRAFFIC_MANAGER_ERROR
				msg = mgrErr.Error()
			}
		}
	}
	return ie, msg
}

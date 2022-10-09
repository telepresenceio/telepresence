package userd

import (
	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// A Service is one that runs during the entire lifecycle of the daemon.
// This should be used to augment the daemon with GRPC services.
type Service interface {
	// SetManagerClient will assign the manager client that this Service will use when acting as
	// a ManagerServer proxy
	SetManagerClient(manager.ManagerClient, ...grpc.CallOption)
}

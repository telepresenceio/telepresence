package userd

import (
	"context"

	"google.golang.org/grpc"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/remotefs"
)

// A Service is one that runs during the entire lifecycle of the daemon.
// This should be used to augment the daemon with GRPC services.
type Service interface {
	// As will cast this instance to what the given ptr points to, and assign
	// that to the pointer. It will panic if type is not implemented.
	As(ptr any)

	// ListenerAddress returns the address that this service is listening to.
	ListenerAddress(ctx context.Context) string

	Server() *grpc.Server

	// FuseFTPMgr returns the manager responsible for creating a client that can connect to the FuseFTP service.
	FuseFTPMgr() remotefs.FuseFTPManager

	RootSessionInProcess() bool
	TeleroutePort() uint16
	WithSession(func(Session) error) error

	PostConnectRequest(context.Context, ConnectRequest) error
	ReadConnectResponse(context.Context) (*rpc.ConnectInfo, error)
	InitFTPServer(context.Context) error
	ManageSessions(context.Context) error
}

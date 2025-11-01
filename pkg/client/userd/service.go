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
	// ListenerAddress returns the address that this service is listening to.
	ListenerAddress(ctx context.Context) string

	Server() *grpc.Server

	ConnectorServer() rpc.ConnectorServer

	// FuseFTPMgr returns the manager responsible for creating a client that can connect to the FuseFTP service.
	FuseFTPMgr() remotefs.FuseFTPManager

	RootSessionInProcess() bool
	TeleroutePort() uint16

	LinkedFTP() bool

	InitFTPServer(context.Context) error
}

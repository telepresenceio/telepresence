package connpool

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// Handler for the MuxTunnel
// Deprecated
type Handler interface {
	tunnel.Handler

	HandleMessage(ctx context.Context, message Message)
}

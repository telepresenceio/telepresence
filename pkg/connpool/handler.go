package connpool

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

type Handler interface {
	tunnel.Handler
	HandleMessage(ctx context.Context, message Message)
}

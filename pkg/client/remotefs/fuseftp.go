package remotefs

import (
	"context"
	_ "embed"

	"github.com/telepresenceio/go-fuseftp/rpc"
)

type FuseFTPManager interface {
	LinkedFTP() bool
	DeferInit(ctx context.Context) error
	GetFuseFTPClient(ctx context.Context) rpc.FuseFTPClient
}

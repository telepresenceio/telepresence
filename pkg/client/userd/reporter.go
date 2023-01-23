package userd

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
)

//go:generate mockgen -destination=mocks/reporter_mock.go . Reporter
type Reporter interface {
	Report(ctx context.Context, action string, entries ...scout.Entry)
	SetMetadatum(ctx context.Context, key string, value any)
	InstallID() string
}

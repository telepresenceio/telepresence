package managerutil

import (
	"context"
)

func ArgoRolloutsEnabled(ctx context.Context) bool {
	return GetEnv(ctx).ArgoRolloutsEnabled
}

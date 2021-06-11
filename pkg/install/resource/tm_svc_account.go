package resource

import (
	"context"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type tmServiceAccount int

var TrafficManagerServiceAccount Instance = tmServiceAccount(0)

func (ri tmServiceAccount) svcAccount(ctx context.Context) *kates.ServiceAccount {
	cr := new(kates.ServiceAccount)
	cr.TypeMeta = kates.TypeMeta{
		Kind:       "ServiceAccount",
		APIVersion: "v1",
	}
	cr.ObjectMeta = kates.ObjectMeta{
		Name:      install.ManagerAppName,
		Namespace: getScope(ctx).namespace,
	}
	return cr
}

func (ri tmServiceAccount) Create(ctx context.Context) error {
	return create(ctx, ri.svcAccount(ctx))
}

func (ri tmServiceAccount) Exists(ctx context.Context) (bool, error) {
	return exists(ctx, ri.svcAccount(ctx))
}

func (ri tmServiceAccount) Delete(ctx context.Context) error {
	return remove(ctx, ri.svcAccount(ctx))
}

func (ri tmServiceAccount) Update(_ context.Context) error {
	// Noop
	return nil
}

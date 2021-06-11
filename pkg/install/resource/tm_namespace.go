package resource

import (
	"context"

	"github.com/datawire/ambassador/pkg/kates"
)

type nsResource int

const TrafficManagerNamespace = nsResource(0)
const TrafficManagerNamespaceKeep = nsResource(1)

var _ Instance = TrafficManagerNamespace

func (ri *nsResource) ns(ctx context.Context) *kates.Namespace {
	cr := new(kates.Namespace)
	cr.TypeMeta = kates.TypeMeta{
		Kind:       "Namespace",
		APIVersion: "v1",
	}
	cr.ObjectMeta = kates.ObjectMeta{
		Name: getScope(ctx).namespace,
	}
	return cr
}

func (ri nsResource) Create(ctx context.Context) error {
	return create(ctx, ri.ns(ctx))
}

func (ri nsResource) Exists(ctx context.Context) (bool, error) {
	return exists(ctx, ri.ns(ctx))
}

func (ri nsResource) Delete(ctx context.Context) error {
	if ri == TrafficManagerNamespaceKeep {
		return nil
	}
	return remove(ctx, ri.ns(ctx))
}

func (ri nsResource) Update(_ context.Context) error {
	// Noop
	return nil
}

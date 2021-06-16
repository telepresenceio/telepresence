package resource

import (
	"context"

	rbac "k8s.io/api/rbac/v1"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type tmRoleBinding int

const TrafficManagerRoleBinding = tmRoleBinding(0)

var _ Instance = TrafficManagerRoleBinding

func (ri tmRoleBinding) roleBinding() *kates.RoleBinding {
	cr := new(kates.RoleBinding)
	cr.TypeMeta = kates.TypeMeta{
		Kind:       "RoleBinding",
		APIVersion: "rbac.authorization.k8s.io/v1",
	}
	cr.ObjectMeta = kates.ObjectMeta{
		Name: install.ManagerAppName,
	}
	return cr
}

func (ri tmRoleBinding) Create(ctx context.Context) error {
	clb := ri.roleBinding()
	clb.Subjects = []rbac.Subject{
		{
			Kind:      "ServiceAccount",
			Name:      "traffic-manager",
			Namespace: getScope(ctx).namespace,
		},
	}
	clb.RoleRef = rbac.RoleRef{
		APIGroup: "rbac.authorization.k8s.io",
		Kind:     "Role",
		Name:     install.ManagerAppName,
	}
	return create(ctx, clb)
}

func (ri tmRoleBinding) Exists(ctx context.Context) (bool, error) {
	return exists(ctx, ri.roleBinding())
}

func (ri tmRoleBinding) Delete(ctx context.Context) error {
	return remove(ctx, ri.roleBinding())
}

func (ri tmRoleBinding) Update(_ context.Context) error {
	// Noop
	return nil
}

package resource

import (
	"context"

	rbac "k8s.io/api/rbac/v1"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type tmClusterRoleBinding int

const TrafficManagerClusterRoleBinding = tmClusterRoleBinding(0)

var _ Instance = TrafficManagerClusterRoleBinding

func (ri tmClusterRoleBinding) roleBinding() *kates.ClusterRoleBinding {
	cr := new(kates.ClusterRoleBinding)
	cr.TypeMeta = kates.TypeMeta{
		Kind:       "ClusterRoleBinding",
		APIVersion: "rbac.authorization.k8s.io/v1",
	}
	cr.ObjectMeta = kates.ObjectMeta{
		Name: install.ManagerAppName,
	}
	return cr
}

func (ri tmClusterRoleBinding) Create(ctx context.Context) error {
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
		Kind:     "ClusterRole",
		Name:     install.ManagerAppName,
	}
	return create(ctx, clb)
}

func (ri tmClusterRoleBinding) Exists(ctx context.Context) (bool, error) {
	return exists(ctx, ri.roleBinding())
}

func (ri tmClusterRoleBinding) Delete(ctx context.Context) error {
	return remove(ctx, ri.roleBinding())
}

func (ri tmClusterRoleBinding) Update(_ context.Context) error {
	// Noop
	return nil
}

package resource

import (
	"context"
	"fmt"

	"github.com/datawire/dlib/dlog"

	rbac "k8s.io/api/rbac/v1"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type tmClusterRole struct {
	found *kates.ClusterRole
}

func NewTrafficManagerClusterRole() Instance {
	return &tmClusterRole{}
}

func (ri tmClusterRole) clusterRole() *kates.ClusterRole {
	cr := new(kates.ClusterRole)
	cr.TypeMeta = kates.TypeMeta{
		Kind:       "ClusterRole",
		APIVersion: "rbac.authorization.k8s.io/v1",
	}
	cr.ObjectMeta = kates.ObjectMeta{
		Name: install.ManagerAppName,
	}
	return cr
}

func (ri tmClusterRole) desiredClusterRole() *kates.ClusterRole {
	cl := ri.clusterRole()
	cl.Rules = []rbac.PolicyRule{
		{
			Verbs:     []string{"get", "list"},
			APIGroups: []string{""},
			Resources: []string{"services"},
		},
		{
			Verbs:     []string{"list", "get", "watch"},
			APIGroups: []string{""},
			Resources: []string{"nodes"},
		},
		{
			Verbs:     []string{"list", "get", "watch"},
			APIGroups: []string{""},
			Resources: []string{"pods"},
		},
	}
	return cl
}

func (ri tmClusterRole) Create(ctx context.Context) error {
	return create(ctx, ri.desiredClusterRole())
}

func (ri *tmClusterRole) Exists(ctx context.Context) (bool, error) {
	found, err := find(ctx, ri.clusterRole())
	if err != nil {
		return false, err
	}
	if found == nil {
		return false, nil
	}
	ri.found = found.(*kates.ClusterRole)
	return true, nil
}

func (ri tmClusterRole) Delete(ctx context.Context) error {
	return remove(ctx, ri.clusterRole())
}

func (ri tmClusterRole) Update(ctx context.Context) error {
	if ri.found == nil {
		return nil
	}

	dcr := ri.desiredClusterRole()
	if rulesEqual(ri.found.Rules, dcr.Rules) {
		return nil
	}

	dcr.ResourceVersion = ri.found.ResourceVersion
	dlog.Infof(ctx, "Updating %s", logName(dcr))
	if err := getScope(ctx).client.Update(ctx, dcr, dcr); err != nil {
		return fmt.Errorf("failed to update %s: %w", logName(dcr), err)
	}
	return nil
}

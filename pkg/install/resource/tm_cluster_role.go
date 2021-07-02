package resource

import (
	"context"
	"fmt"

	rbac "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type tmClusterRole struct {
	found *kates.ClusterRole
}

func NewTrafficManagerClusterRole() Instance {
	return &tmClusterRole{}
}

func (ri tmClusterRole) clusterRole(ctx context.Context) *kates.ClusterRole {
	cr := new(kates.ClusterRole)
	cr.TypeMeta = kates.TypeMeta{
		Kind:       "ClusterRole",
		APIVersion: "rbac.authorization.k8s.io/v1",
	}
	cr.ObjectMeta = kates.ObjectMeta{
		Name: fmt.Sprintf("%s-%s", install.ManagerAppName, getScope(ctx).namespace),
	}
	return cr
}

func (ri tmClusterRole) desiredClusterRole(ctx context.Context) *kates.ClusterRole {
	cl := ri.clusterRole(ctx)
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
	return create(ctx, ri.desiredClusterRole(ctx))
}

func (ri *tmClusterRole) Exists(ctx context.Context) (bool, error) {
	found, err := find(ctx, ri.clusterRole(ctx))
	if err != nil {
		if errors.IsForbidden(err) {
			// Simply assume that it exists. Not much else we can do unless
			// RBAC is configured to give access.
			return true, nil
		}
		return false, err
	}
	if found == nil {
		return false, nil
	}
	ri.found = found.(*kates.ClusterRole)
	return true, nil
}

func (ri tmClusterRole) Delete(ctx context.Context) error {
	return remove(ctx, ri.clusterRole(ctx))
}

func (ri tmClusterRole) Update(ctx context.Context) error {
	if ri.found == nil {
		return nil
	}
	if isManagedByHelm(ctx, ri.found) {
		return nil
	}

	dcr := ri.desiredClusterRole(ctx)
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

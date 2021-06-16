package resource

import (
	"context"
	"fmt"

	"github.com/datawire/dlib/dlog"

	rbac "k8s.io/api/rbac/v1"

	"github.com/datawire/ambassador/pkg/kates"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
)

type tmRole struct {
	found *kates.Role
}

func NewTrafficManagerRole() Instance {
	return &tmRole{}
}

func (ri tmRole) role(ctx context.Context) *kates.Role {
	cr := new(kates.Role)
	cr.TypeMeta = kates.TypeMeta{
		Kind:       "Role",
		APIVersion: "rbac.authorization.k8s.io/v1",
	}
	cr.ObjectMeta = kates.ObjectMeta{
		Name:      install.ManagerAppName,
		Namespace: getScope(ctx).namespace,
	}
	return cr
}

func (ri tmRole) desiredRole(ctx context.Context) *kates.Role {
	cl := ri.role(ctx)
	cl.Rules = []rbac.PolicyRule{
		{
			Verbs:     []string{"create"},
			APIGroups: []string{""},
			Resources: []string{"services"},
		},
	}
	return cl
}

func (ri tmRole) Create(ctx context.Context) error {
	return create(ctx, ri.desiredRole(ctx))
}

func (ri *tmRole) Exists(ctx context.Context) (bool, error) {
	found, err := find(ctx, ri.role(ctx))
	if err != nil {
		return false, err
	}
	if found == nil {
		return false, nil
	}
	ri.found = found.(*kates.Role)
	return true, nil
}

func (ri tmRole) Delete(ctx context.Context) error {
	return remove(ctx, ri.role(ctx))
}

func (ri tmRole) Update(ctx context.Context) error {
	if ri.found == nil {
		return nil
	}

	dcr := ri.desiredRole(ctx)
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

func rulesEqual(ar []rbac.PolicyRule, br []rbac.PolicyRule) bool {
	if len(ar) != len(br) {
		return false
	}

	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i, as := range a {
			if as != b[i] {
				return false
			}
		}
		return true
	}

	for i, a := range ar {
		b := br[i]
		if !(eq(a.APIGroups, b.APIGroups) && eq(a.ResourceNames, b.ResourceNames) && eq(a.Resources, b.Resources) && eq(a.Verbs, b.Verbs)) {
			return false
		}
	}
	return false
}

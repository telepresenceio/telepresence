package k8sapi

import (
	"context"
	"fmt"

	auth "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/clog"
)

func CanI(ctx context.Context, ras ...*auth.ResourceAttributes) (bool, error) {
	authHandler := GetK8sInterface(ctx).AuthorizationV1().SelfSubjectAccessReviews()
	for _, ra := range ras {
		review := auth.SelfSubjectAccessReview{Spec: auth.SelfSubjectAccessReviewSpec{ResourceAttributes: ra}}
		ar, err := authHandler.Create(ctx, &review, meta.CreateOptions{})
		if err == nil && ar.Status.Allowed {
			continue
		}
		where := ""
		if ra.Namespace != "" {
			where = " in namespace " + ra.Namespace
		}
		if err != nil {
			err = fmt.Errorf(`unable to do "can-i %s %s%s": %v`, ra.Verb, ra.Resource, where, err)
			if ctx.Err() == nil {
				clog.Error(ctx, err)
			}
		} else {
			clog.Infof(ctx, `"can-i %s %s%s" is not allowed`, ra.Verb, ra.Resource, where)
		}
		return false, err
	}
	return true, nil
}

func CanWatch(ctx context.Context, group, resource, name, ns string) bool {
	if name == "" {
		ok, err := CanI(ctx, &auth.ResourceAttributes{
			Verb:      "list",
			Resource:  resource,
			Group:     group,
			Namespace: ns,
		})
		if err != nil || !ok {
			return false
		}
	}
	ok, err := CanI(ctx, &auth.ResourceAttributes{
		Verb:      "watch",
		Resource:  resource,
		Name:      name,
		Group:     group,
		Namespace: ns,
	})
	if err != nil || !ok {
		return false
	}
	return true
}

// CanWatchNamespaces answers the question if this client has the RBAC permissions necessary
// to watch namespaces. The answer is likely false when using a namespaces scoped installation.
func CanWatchNamespaces(ctx context.Context) bool {
	return CanWatch(ctx, "", "namespaces", "", "")
}

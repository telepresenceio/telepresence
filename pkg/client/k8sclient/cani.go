package k8sclient

import (
	"context"
	"fmt"

	v1 "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
)

func CanI(ctx context.Context, ra *v1.ResourceAttributes) (bool, error) {
	authHandler := k8sapi.GetK8sInterface(ctx).AuthorizationV1().SelfSubjectAccessReviews()
	review := v1.SelfSubjectAccessReview{Spec: v1.SelfSubjectAccessReviewSpec{ResourceAttributes: ra}}
	ar, err := authHandler.Create(ctx, &review, meta.CreateOptions{})
	if err == nil && ar.Status.Allowed {
		return true, nil
	}
	where := ""
	if ra.Namespace != "" {
		where = " in namespace " + ra.Namespace
	}
	if err != nil {
		err = fmt.Errorf(`unable to do "can-i %s %s%s": %v`, ra.Verb, ra.Resource, where, err)
		if ctx.Err() == nil {
			dlog.Error(ctx, err)
		}
	} else {
		dlog.Infof(ctx, `"can-i %s %s%s" is not allowed`, ra.Verb, ra.Resource, where)
	}
	return false, err
}

// CanWatchNamespaces answers the question if this client has the RBAC permissions necessary
// to watch namespaces. The answer is likely false when using a namespaces scoped installation.
func CanWatchNamespaces(ctx context.Context) bool {
	ok, err := CanI(ctx, &v1.ResourceAttributes{
		Verb:     "watch",
		Resource: "namespaces",
	})
	return err == nil && ok
}

// CanPortForward answers the question if this client has the RBAC permissions necessary
// to perform a port-forward to the connected namespace.
func CanPortForward(ctx context.Context, namespace string) bool {
	ok, err := CanI(ctx, &v1.ResourceAttributes{
		Verb:        "create",
		Resource:    "pods",
		Subresource: "portforward",
		Namespace:   namespace,
	})
	return err == nil && ok
}

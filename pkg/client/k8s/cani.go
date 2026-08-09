package k8s

import (
	"context"

	v1 "k8s.io/api/authorization/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// portForwardAttributes builds the pods/portforward ResourceAttributes for
// namespace, optionally scoped to a specific pod name.
func portForwardAttributes(namespace string, podName ...string) *v1.ResourceAttributes {
	attr := &v1.ResourceAttributes{
		Verb:        "create",
		Resource:    "pods",
		Subresource: "portforward",
		Namespace:   namespace,
	}
	if len(podName) > 0 {
		attr.Name = podName[0]
	}
	return attr
}

// CanPortForward answers the question if this client has the RBAC permissions necessary
// to perform a port-forward to the connected namespace, optionally scoped to a specific
// pod name.
func CanPortForward(ctx context.Context, namespace string, podName ...string) bool {
	ok, err := k8sapi.CanI(ctx, portForwardAttributes(namespace, podName...))
	return err == nil && ok
}

package k8s

import (
	v1 "k8s.io/api/authorization/v1"
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

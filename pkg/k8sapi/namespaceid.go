package k8sapi

import (
	"context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ZeroNamespaceID is the ID used when the UID of a namespace cannot be determined.
const ZeroNamespaceID = "00000000-0000-0000-0000-000000000000"

// GetNamespaceID returns the uuid for a given namespace.  If there is an error, it still
// returns a usable ID along with the error.
func GetNamespaceID(ctx context.Context, namespace string) (clusterID string, err error) {
	ns, err := GetK8sInterface(ctx).CoreV1().Namespaces().Get(ctx, namespace, v1.GetOptions{})
	if err != nil {
		// But still return a usable ID if there's an error.
		return ZeroNamespaceID, err
	}
	return string(ns.GetUID()), nil
}

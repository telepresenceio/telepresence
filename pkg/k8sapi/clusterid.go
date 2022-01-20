package k8sapi

import (
	"context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GetClusterID returns the cluster ID for the given cluster.  If there is an error, it still
// returns a usable ID along with the error.
func GetClusterID(ctx context.Context) (clusterID string, err error) {
	// Get the ID of the "default" Namespace.
	ns, err := GetK8sInterface(ctx).CoreV1().Namespaces().Get(ctx, "default", v1.GetOptions{})
	if err != nil {
		// But still return a usable ID if there's an error.
		return "00000000-0000-0000-0000-000000000000", err
	}
	return string(ns.GetUID()), nil
}

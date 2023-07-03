package cluster

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/datawire/dlib/dlog"
)

var GetIDFunc = GetID //nolint:gochecknoglobals // extension point

func GetID(ctx context.Context, client v1.CoreV1Interface, namespace string) (string, error) {
	// Get the clusterID from the default namespace, or from the manager's namespace if
	// the traffic-manager doesn't have access to the default namespace.
	cid, err := idFromNamespace(ctx, client, "default")
	if err == nil {
		return cid, nil
	}

	dlog.Infof(ctx, "unable to get namespace \"default\", will try %q instead: %v", namespace, err)

	return idFromNamespace(ctx, client, namespace)
}

func idFromNamespace(ctx context.Context, client v1.CoreV1Interface, namespace string) (string, error) {
	opts := metav1.GetOptions{}
	ns, err := client.Namespaces().Get(ctx, namespace, opts)
	if err == nil {
		return string(ns.GetUID()), nil
	}

	return "", err
}

package cluster

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

var GetIDFunc = GetID //nolint:gochecknoglobals // extension point

func GetID(ctx context.Context, client v1.CoreV1Interface, namespace string) (string, error) {
	// change: old IDs were generated from default ns
	// now always generate ID from manager namespace
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

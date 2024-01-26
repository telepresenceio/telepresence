package cluster

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

var GetInstallIDFunc = GetNamespaceID //nolint:gochecknoglobals // extension point

func GetNamespaceID(ctx context.Context, client v1.CoreV1Interface, namespace string) (string, error) {
	ns, err := client.Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return string(ns.GetUID()), nil
	}
	return "", err
}

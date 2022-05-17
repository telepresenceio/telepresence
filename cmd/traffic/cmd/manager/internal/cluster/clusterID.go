package cluster

import (
	"context"
	"fmt"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/license"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

func getClusterID(ctx context.Context, client v1.CoreV1Interface, namespace string) (string, error) {
	// Get the clusterID from the default namespace, or from the manager's namespace if
	// the traffic-manager doesn't have access to the default namespace.
	cid, err := clusterIDFromNamespace(ctx, client, "default")
	if err == nil {
		return cid, nil
	}

	dlog.Infof(ctx, "unable to get namespace \"default\", will try %q instead: %v", namespace, err)

	cid, err = clusterIDFromNamespace(ctx, client, namespace)
	if err == nil {
		return cid, nil
	}

	lb := license.BundleFromContext(ctx)
	if lb == nil {
		return "", err
	}

	dlog.Infof(ctx, "unable to get namespace %q, will try license instead: %v", namespace, err)

	claims, err := lb.GetLicenseClaims()
	if err != nil {
		return "", fmt.Errorf("unable to read license claims: %w", err)
	}

	cid, err = claims.GetClusterID()
	if err != nil {
		return "", fmt.Errorf("unable to get cluster id from license claims: %w", err)
	}

	return cid, err
}

func clusterIDFromNamespace(ctx context.Context, client v1.CoreV1Interface, namespace string) (string, error) {
	opts := metav1.GetOptions{}
	ns, err := client.Namespaces().Get(ctx, namespace, opts)
	if err == nil {
		return string(ns.GetUID()), nil
	}

	return "", err
}

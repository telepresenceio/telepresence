package cluster

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/license"
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

	cid, err = idFromNamespace(ctx, client, namespace)
	if err == nil {
		return cid, nil
	}

	lb := license.BundleFromContext(ctx)
	if lb == nil {
		return "", fmt.Errorf("license not found: %v", err)
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

func idFromNamespace(ctx context.Context, client v1.CoreV1Interface, namespace string) (string, error) {
	opts := metav1.GetOptions{}
	ns, err := client.Namespaces().Get(ctx, namespace, opts)
	if err == nil {
		return string(ns.GetUID()), nil
	}

	return "", err
}

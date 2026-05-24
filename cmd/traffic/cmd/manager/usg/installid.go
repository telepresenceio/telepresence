// Package usg wires the anonymous usage producer for the traffic-manager.
//
// The manager identifies itself by a UUID that lives in a ConfigMap it owns
// in its own namespace. The ConfigMap is created on first start and reused
// across restarts and Helm upgrades. A fresh install (i.e. one that follows a
// `helm uninstall` removing the namespace contents) gets a new UUID, which
// is the desired behavior — a new install is in fact a new install.
package usg

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	core "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const (
	// ConfigMapName is the name of the ConfigMap that holds install-scope
	// data for the traffic-manager. It is created in the manager's own
	// namespace on first start.
	ConfigMapName = "traffic-manager-install"

	// InstallIDKey is the key inside the ConfigMap's data map that holds
	// the manager's installation UUID.
	InstallIDKey = "installId"
)

// LoadOrCreateInstallID returns the manager's installation ID, creating it on
// first call. The id is persisted in a ConfigMap in namespace.
//
// Concurrent creators race safely: if Create returns AlreadyExists, the
// existing object's value wins. The returned id is always a non-empty UUID
// string.
func LoadOrCreateInstallID(ctx context.Context, namespace string) (string, error) {
	api := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(namespace)

	existing, err := api.Get(ctx, ConfigMapName, meta.GetOptions{})
	switch {
	case err == nil:
		if id := strings.TrimSpace(existing.Data[InstallIDKey]); id != "" {
			return id, nil
		}
		// ConfigMap exists but lacks the id; fall through and patch it.
	case !k8serr.IsNotFound(err):
		return "", err
	}

	id := uuid.NewString()
	cm := &core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{
			Name:      ConfigMapName,
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "traffic-manager",
				"app.kubernetes.io/component": "usage",
			},
		},
		Data: map[string]string{InstallIDKey: id},
	}
	if _, err := api.Create(ctx, cm, meta.CreateOptions{}); err != nil {
		if k8serr.IsAlreadyExists(err) {
			// Lost a race; re-read.
			existing, getErr := api.Get(ctx, ConfigMapName, meta.GetOptions{})
			if getErr != nil {
				return "", getErr
			}
			if got := strings.TrimSpace(existing.Data[InstallIDKey]); got != "" {
				return got, nil
			}
			return "", errors.New("usg: install-id ConfigMap exists but is empty")
		}
		return "", err
	}
	return id, nil
}

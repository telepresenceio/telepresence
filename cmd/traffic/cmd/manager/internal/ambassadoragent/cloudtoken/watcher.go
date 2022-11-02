package cloudtoken

import (
	"context"
	"strings"
	"sync"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type tokenWatchers struct {
	mapsWatcher    *k8sapi.Watcher[*corev1.ConfigMap]
	secretsWatcher *k8sapi.Watcher[*corev1.Secret]
}

func newTokenWatchers(clientset kubernetes.Interface, watchedNs string) *tokenWatchers {
	client := clientset.CoreV1().RESTClient()

	mapsWatcher := k8sapi.NewWatcher(
		"configmaps",
		client,
		&sync.Cond{
			L: &sync.Mutex{},
		},
		k8sapi.WithNamespace[*corev1.ConfigMap](watchedNs),
		k8sapi.WithEquals(func(cm1, cm2 *corev1.ConfigMap) bool {
			_, ok := cm2.Data[CLOUD_TOKEN_KEY]
			return cm1.Name == cm2.Name && !ok
		}),
	)

	secretsWatcher := k8sapi.NewWatcher(
		"secrets",
		client,
		&sync.Cond{
			L: &sync.Mutex{},
		},
		k8sapi.WithNamespace[*corev1.Secret](watchedNs),
		k8sapi.WithEquals(func(s1, s2 *corev1.Secret) bool {
			_, ok := s2.Data[CLOUD_TOKEN_KEY]
			return s1.Name == s2.Name && !ok
		}),
	)

	return &tokenWatchers{
		mapsWatcher:    mapsWatcher,
		secretsWatcher: secretsWatcher,
	}
}

// search checks if apikey token exists, if it does, shut down the watchers
func (w *tokenWatchers) searchMaps(ctx context.Context, cancel context.CancelFunc) {
	configmaps, err := w.mapsWatcher.List(ctx)
	if err != nil {
		dlog.Errorf(ctx, "error watching cloud token configmaps: %s", err.Error())
		cancel()
	}
	for _, cm := range configmaps {
		_, ok := cm.Data[CLOUD_TOKEN_KEY]
		if strings.HasSuffix(cm.Name, CLOUD_TOKEN_NAME_SUFFIX) && ok {
			dlog.Infof(ctx, "configmap %s found, stopping cloud token watchers", cm.Name)
			cancel()
		}
	}
}

func (w *tokenWatchers) searchSecrets(ctx context.Context, cancel context.CancelFunc) {
	secrets, err := w.secretsWatcher.List(ctx)
	if err != nil {
		dlog.Errorf(ctx, "error watching cloud token secrets: %s", err.Error())
		cancel()
	}
	for _, s := range secrets {
		_, ok := s.Data[CLOUD_TOKEN_KEY]
		if strings.HasSuffix(s.Name, CLOUD_TOKEN_NAME_SUFFIX) && ok {
			dlog.Infof(ctx, "secret %s found, stopping cloud token watchers", s.Name)
			cancel()
		}
	}
}

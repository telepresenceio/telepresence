package cloudtoken

import (
	"context"
	"strings"
	"sync"

	"k8s.io/client-go/rest"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type tokenWatchers struct {
	mapsCond    *sync.Cond
	mapsWatcher *k8sapi.Watcher[*corev1.ConfigMap]
}

func newTokenWatchers(clientset kubernetes.Interface, watchedNs string) *tokenWatchers {
	client := clientset.CoreV1().RESTClient()
	if client == (*rest.RESTClient)(nil) {
		// This happens when unit tests run because the fake clientset doesn't provide
		// rest clients.
		return nil
	}

	mapsCond := &sync.Cond{
		L: &sync.Mutex{},
	}

	mapsWatcher := k8sapi.NewWatcher(
		"configmaps",
		client,
		mapsCond,
		k8sapi.WithEquals[*corev1.ConfigMap](func(cm1, cm2 *corev1.ConfigMap) bool {
			_, ok := cm2.Data[CLOUD_TOKEN_KEY]
			return cm1.Name == cm2.Name && !ok
		}),
		k8sapi.WithNamespace[*corev1.ConfigMap](watchedNs),
	)

	return &tokenWatchers{
		mapsCond:    mapsCond,
		mapsWatcher: mapsWatcher,
	}
}

// search checks if apikey token exists, if it does, shut down the watchers.
func (w *tokenWatchers) searchMaps(ctx context.Context, cancel context.CancelFunc) {
	configmaps, err := w.mapsWatcher.List(ctx)
	if err != nil {
		dlog.Errorf(ctx, "error watching configmaps: %s", err)
	}
	for _, cm := range configmaps {
		_, ok := cm.Data[CLOUD_TOKEN_KEY]
		if strings.HasSuffix(cm.Name, CLOUD_TOKEN_NAME_SUFFIX) && ok {
			dlog.Infof(ctx, "configmap %s found, stopping cloud token watchers", cm.Name)
			cancel()
		}
	}
}

package cloudtoken

import (
	"context"
	"strings"
	"sync"

	"k8s.io/client-go/rest"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
)

type tokenWatchers struct {
	mapsCond    *sync.Cond
	mapsWatcher *k8sapi.Watcher
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
		watchedNs,
		client,
		&corev1.ConfigMap{},
		mapsCond,
		func(cm1, cm2 runtime.Object) bool {
			_, ok := cm2.(*corev1.ConfigMap).Data[CLOUD_TOKEN_KEY]
			return cm1.(*corev1.ConfigMap).Name == cm2.(*corev1.ConfigMap).Name && !ok
		},
	)

	return &tokenWatchers{
		mapsCond:    mapsCond,
		mapsWatcher: mapsWatcher,
	}
}

// search checks if apikey token exists, if it does, shut down the watchers.
func (w *tokenWatchers) searchMaps(ctx context.Context, cancel context.CancelFunc) {
	configmaps := w.mapsWatcher.List(ctx)
	for _, cm := range configmaps {
		_, ok := cm.(*corev1.ConfigMap).Data[CLOUD_TOKEN_KEY]
		if strings.HasSuffix(cm.(*corev1.ConfigMap).Name, CLOUD_TOKEN_NAME_SUFFIX) && ok {
			dlog.Infof(ctx, "configmap %s found, stopping cloud token watchers", cm.(*corev1.ConfigMap).Name)
			cancel()
		}
	}
}

func subscribe(c context.Context, cond *sync.Cond) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		for {
			cond.L.Lock()
			cond.Wait()
			cond.L.Unlock()

			select {
			case <-c.Done():
				close(ch)
				return
			case ch <- struct{}{}:
			}
		}
	}()
	return ch
}

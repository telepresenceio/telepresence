package cloudtoken

import (
	"context"
	"sync"

	"github.com/datawire/k8sapi/pkg/k8sapi"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

type tokenWatchers struct {
	cond           *sync.Cond
	mapsWatcher    *k8sapi.Watcher[*corev1.ConfigMap]
	secretsWatcher *k8sapi.Watcher[*corev1.Secret]
}

func newTokenWatchers(clientset kubernetes.Interface, watchedNs string) *tokenWatchers {
	client := clientset.CoreV1().RESTClient()

	cond := &sync.Cond{
		L: &sync.Mutex{},
	}

	return &tokenWatchers{
		mapsWatcher: k8sapi.NewWatcher("configmaps", client, cond,
			k8sapi.WithNamespace[*corev1.ConfigMap](watchedNs),
			k8sapi.WithEquals(func(cm1, cm2 *corev1.ConfigMap) bool {
				_, ok := cm2.Data[cloudConnectTokenKey]
				return cm1.Name == cm2.Name && !ok
			}),
		),
		secretsWatcher: k8sapi.NewWatcher("secrets", client, cond,
			k8sapi.WithNamespace[*corev1.Secret](watchedNs),
			k8sapi.WithEquals(func(s1, s2 *corev1.Secret) bool {
				_, ok := s2.Data[cloudConnectTokenKey]
				return s1.Name == s2.Name && !ok
			}),
		),
		cond: cond,
	}
}

func (w *tokenWatchers) subscribe(ctx context.Context) <-chan struct{} {
	return k8sapi.Subscribe(ctx, w.cond)
}

package cliconfig

import (
	"context"
	"fmt"
	"sync"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
)

const (
	cfgFileName      = "client.yaml"
	cfgConfigMapName = "traffic-manager"
)

type WatcherCallback func(watch.EventType, runtime.Object) error

type Watcher interface {
	Run(ctx context.Context) error
	GetConfigYaml() []byte
}

type config struct {
	sync.RWMutex
	namespace string
	cfgYaml   []byte

	callbacks []WatcherCallback
}

func NewWatcher(namespace string, callbacks ...WatcherCallback) Watcher {
	return &config{
		namespace: namespace,
		callbacks: callbacks,
	}
}

func (c *config) Run(ctx context.Context) error {
	dlog.Infof(ctx, "Started watcher for ConfigMap %s", cfgConfigMapName)
	defer dlog.Infof(ctx, "Ended watcher for ConfigMap %s", cfgConfigMapName)

	// The Watch will perform a http GET call to the kubernetes API server, and that connection will not remain open forever
	// so when it closes, the watch must start over. This goes on until the context is cancelled.
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	for ctx.Err() == nil {
		w, err := api.ConfigMaps(c.namespace).Watch(ctx, meta.SingleObject(meta.ObjectMeta{Name: cfgConfigMapName}))
		if err != nil {
			return fmt.Errorf("unable to create configmap watcher: %v", err)
		}
		if !c.configMapEventHandler(ctx, w.ResultChan()) {
			return nil
		}
	}
	return nil
}

func (c *config) configMapEventHandler(ctx context.Context, evCh <-chan watch.Event) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case event, ok := <-evCh:
			if !ok {
				return true // restart watcher
			}
			switch event.Type {
			case watch.Deleted:
				if m, ok := event.Object.(*core.ConfigMap); ok {
					dlog.Debugf(ctx, "%s %s", event.Type, m.Name)
					c.refreshFile(ctx, nil)
				}
			case watch.Added, watch.Modified:
				if m, ok := event.Object.(*core.ConfigMap); ok {
					dlog.Debugf(ctx, "%s %s", event.Type, m.Name)
					c.refreshFile(ctx, m.Data)
				}
			}

			for _, cb := range c.callbacks {
				if err := cb(event.Type, event.Object); err != nil {
					dlog.Debugf(ctx, "watcher callback error: %s", err.Error())
				}
			}
		}
	}
}

func (c *config) refreshFile(ctx context.Context, data map[string]string) {
	c.Lock()
	if yml, ok := data[cfgFileName]; ok {
		c.cfgYaml = []byte(yml)
		dlog.Debugf(ctx, "Refreshed client config: %s", yml)
	} else {
		c.cfgYaml = nil
		dlog.Debugf(ctx, "Cleared client config")
	}
	c.Unlock()
}

func (c *config) GetConfigYaml() (ret []byte) {
	c.RLock()
	ret = c.cfgYaml
	c.RUnlock()
	return
}

package config

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
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

const (
	clientConfigFileName = "client.yaml"
	cfgConfigMapName     = "traffic-manager"
)

type WatcherCallback func(watch.EventType, runtime.Object) error

type Watcher interface {
	Run(ctx context.Context) error
	GetClientConfigYaml() []byte
}

type config struct {
	sync.RWMutex
	namespace string

	clientYAML []byte
}

func NewWatcher(namespace string) Watcher {
	return &config{
		namespace: namespace,
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
		}
	}
}

func (c *config) refreshFile(ctx context.Context, data map[string]string) {
	c.Lock()
	if yml, ok := data[clientConfigFileName]; ok {
		c.clientYAML = []byte(yml)
		env := managerutil.GetEnv(ctx)
		if len(env.ManagedNamespaces) > 0 {
			dlog.Debugf(ctx, "Checking if Augment mapped namespaces with %d managed namespaces", len(env.ManagedNamespaces))
			cfg, err := client.ParseConfigYAML(c.clientYAML)
			if err != nil {
				dlog.Errorf(ctx, "failed to unmarshal YAML from %s", clientConfigFileName)
			}
			if len(cfg.Cluster().MappedNamespaces) == 0 {
				dlog.Debugf(ctx, "Augment mapped namespaces with %d managed namespaces", len(env.ManagedNamespaces))
				cfg.Cluster().MappedNamespaces = env.ManagedNamespaces
				yml = cfg.String()
				c.clientYAML = []byte(yml)
			}
		}
		dlog.Debugf(ctx, "Refreshed client config: %s", yml)
	} else {
		c.clientYAML = nil
		dlog.Debugf(ctx, "Cleared client config")
	}
	c.Unlock()
}

func (c *config) GetClientConfigYaml() (ret []byte) {
	c.RLock()
	ret = c.clientYAML
	c.RUnlock()
	return
}

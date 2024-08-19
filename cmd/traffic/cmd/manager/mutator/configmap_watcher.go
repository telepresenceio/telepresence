package mutator

import (
	"context"
	"fmt"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	informerCore "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
)

func tpAgentsInformer(ctx context.Context, ns string) informerCore.ConfigMapInformer {
	f := informer.GetK8sFactory(ctx, ns)
	cV1 := informerCore.New(f, ns, func(options *meta.ListOptions) {
		options.FieldSelector = "metadata.name=" + agentconfig.ConfigMap
	})
	cms := cV1.ConfigMaps()
	return cms
}

func tpAgentsConfigMap(ctx context.Context, ns string) (*core.ConfigMap, error) {
	cm, err := tpAgentsInformer(ctx, ns).Lister().ConfigMaps(ns).Get(agentconfig.ConfigMap)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, fmt.Errorf("unable to get ConfigMap %s: %w", agentconfig.ConfigMap, err)
		}
		cm = nil
	}
	return cm, nil
}

func (c *configWatcher) startConfigMap(ctx context.Context, ns string) cache.SharedIndexInformer {
	ix := tpAgentsInformer(ctx, ns).Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		// Strip of the parts of the service that we don't care about
		if cm, ok := o.(*core.ConfigMap); ok {
			cm.ManagedFields = nil
			cm.Finalizers = nil
			cm.OwnerReferences = nil
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		dlog.Errorf(ctx, "watcher for ConfigMap %s %s: %v", agentconfig.ConfigMap, whereWeWatch(ns), err)
	})
	return ix
}

func (c *configWatcher) watchConfigMap(ctx context.Context, ix cache.SharedIndexInformer) error {
	_, err := ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if cm, ok := obj.(*core.ConfigMap); ok {
					dlog.Debugf(ctx, "ADDED %s.%s", cm.Name, cm.Namespace)
					c.getNamespaceLock(cm.Namespace)
					c.handleAdd(ctx, cm)
				}
			},
			DeleteFunc: func(obj any) {
				cm, ok := obj.(*core.ConfigMap)
				if !ok {
					if dfu, isDfu := obj.(*cache.DeletedFinalStateUnknown); isDfu {
						cm, ok = dfu.Obj.(*core.ConfigMap)
					}
				}
				if ok {
					dlog.Debugf(ctx, "DELETED %s.%s", cm.Name, cm.Namespace)
					c.getNamespaceLock(cm.Namespace)
					c.handleDelete(ctx, cm)
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if cm, ok := newObj.(*core.ConfigMap); ok {
					dlog.Debugf(ctx, "UPDATED %s.%s", cm.Name, cm.Namespace)
					c.handleUpdate(ctx, oldObj.(*core.ConfigMap), cm)
				}
			},
		})
	return err
}

func (c *configWatcher) handleAdd(ctx context.Context, cm *core.ConfigMap) {
	ns := cm.Namespace
	for n, yml := range cm.Data {
		c.handleAddOrUpdateEntry(ctx, entry{
			name:      n,
			namespace: ns,
			value:     yml,
		})
	}
}

func (c *configWatcher) handleDelete(ctx context.Context, cm *core.ConfigMap) {
	ns := cm.Namespace
	for n, yml := range cm.Data {
		c.handleDeleteEntry(ctx, entry{
			name:      n,
			namespace: ns,
			value:     yml,
		})
	}
}

func (c *configWatcher) handleUpdate(ctx context.Context, oldCm, newCm *core.ConfigMap) {
	ns := newCm.Namespace
	for n, newYml := range newCm.Data {
		e := entry{
			name:      n,
			namespace: ns,
			value:     newYml,
		}
		if oldYml, ok := oldCm.Data[n]; ok {
			e.oldValue = oldYml
		}
		c.handleAddOrUpdateEntry(ctx, e)
	}
	for n, oldYml := range oldCm.Data {
		if _, ok := newCm.Data[n]; !ok {
			c.handleDeleteEntry(ctx, entry{
				name:      n,
				namespace: ns,
				value:     oldYml,
			})
		}
	}
}

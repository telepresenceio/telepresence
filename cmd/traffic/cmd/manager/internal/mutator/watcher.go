package mutator

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator/v25uninstall"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type agentInjectorConfig struct {
	Namespaced bool     `json:"namespaced"`
	Namespaces []string `json:"namespaces,omitempty"`
}

type Map interface {
	GetInto(string, string, interface{}) (bool, error)
	Run(context.Context) error
	Store(context.Context, *agentconfig.Sidecar, bool) error
	DeleteMapsAndRolloutAll(ctx context.Context)
	UninstallV25(ctx context.Context)
}

func decode(v string, into interface{}) error {
	return yaml.NewDecoder(strings.NewReader(v)).Decode(into)
}

func Load(ctx context.Context, namespace string) (m Map, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		if err != nil {
			cancel()
		}
	}()

	ac := agentInjectorConfig{}
	cm, err := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(namespace).Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
	if err == nil {
		if v, ok := cm.Data[agentconfig.InjectorKey]; ok {
			err = decode(v, &ac)
			if err != nil {
				return nil, err
			}
			dlog.Infof(ctx, "using %q entry from ConfigMap %s", agentconfig.InjectorKey, agentconfig.ConfigMap)
		}
	}

	dlog.Infof(ctx, "Loading ConfigMaps from %v", ac.Namespaces)
	return NewWatcher(agentconfig.ConfigMap, ac.Namespaces...), nil
}

func (e *entry) workload(ctx context.Context) (*agentconfig.Sidecar, k8sapi.Workload, error) {
	ac := &agentconfig.Sidecar{}
	if err := decode(e.value, ac); err != nil {
		return nil, nil, fmt.Errorf("failed to decode ConfigMap entry %q into an agent config", e.value)
	}
	wl, err := k8sapi.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get %s %s.%s: %v", ac.WorkloadKind, ac.WorkloadName, ac.Namespace, err)
	}
	return ac, wl, nil
}

func triggerRollout(ctx context.Context, wl k8sapi.Workload) {
	if rs, ok := k8sapi.ReplicaSetImpl(wl); ok {
		// Rollout of a replicatset will not recreate the pods. In order for that to happen, the
		// set must be scaled down and then up again.
		replicas := 1
		if rp := rs.Spec.Replicas; rp != nil {
			replicas = int(*rp)
		}
		if replicas > 0 {
			patch := `{"spec": {"replicas": 0}}`
			if err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(patch)); err != nil {
				dlog.Errorf(ctx, "unable to scale ReplicaSet %s.%s to zero: %v", wl.GetName(), wl.GetNamespace(), err)
				return
			}
			patch = fmt.Sprintf(`{"spec": {"replicas": %d}}`, replicas)
			if err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(patch)); err != nil {
				dlog.Errorf(ctx, "unable to scale ReplicaSet %s.%s to %d: %v", wl.GetName(), wl.GetNamespace(), replicas, err)
			}
		}
		return
	}
	restartAnnotation := fmt.Sprintf(
		`{"spec": {"template": {"metadata": {"annotations": {"%srestartedAt": "%s"}}}}}`,
		install.DomainPrefix,
		time.Now().Format(time.RFC3339),
	)
	if err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(restartAnnotation)); err != nil {
		dlog.Errorf(ctx, "unable to patch %s %s.%s: %v", wl.GetKind(), wl.GetName(), wl.GetNamespace(), err)
		return
	}
	dlog.Infof(ctx, "Successfully rolled out %s.%s", wl.GetName(), wl.GetNamespace())
}

func NewWatcher(name string, namespaces ...string) *configWatcher {
	return &configWatcher{
		name:       name,
		namespaces: namespaces,
		data:       make(map[string]map[string]string),
	}
}

type configWatcher struct {
	sync.RWMutex
	cancel     context.CancelFunc
	name       string
	namespaces []string
	data       map[string]map[string]string
	modCh      chan entry
	delCh      chan entry
}

type entry struct {
	name      string
	namespace string
	value     string
}

func (c *configWatcher) Run(ctx context.Context) error {
	ctx, c.cancel = context.WithCancel(ctx)
	addCh, delCh, err := c.Start(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-delCh:
			dlog.Infof(ctx, "del %s.%s: %s", e.name, e.namespace, e.value)
			ac, wl, err := e.workload(ctx)
			if err != nil {
				dlog.Error(ctx, err)
				continue
			}
			if ac.Create {
				// Deleted before it was generated, just ignore
				continue
			}
			triggerRollout(ctx, wl)
		case e := <-addCh:
			dlog.Infof(ctx, "add %s.%s: %s", e.name, e.namespace, e.value)
			ac, wl, err := e.workload(ctx)
			if err != nil {
				dlog.Error(ctx, err)
				continue
			}
			if ac.Create {
				if ac, err = agentmap.Generate(ctx, wl); err != nil {
					dlog.Error(ctx, err)
				} else if err = c.Store(ctx, ac, false); err != nil {
					dlog.Error(ctx, err)
				}
				continue // Calling Store() will generate a new event, so we skip rollout here
			}
			triggerRollout(ctx, wl)
		}
	}
}

func (c *configWatcher) GetInto(key, ns string, into interface{}) (bool, error) {
	c.RLock()
	var v string
	vs, ok := c.data[ns]
	if ok {
		v, ok = vs[key]
	}
	c.RUnlock()
	if !ok {
		return false, nil
	}
	if err := decode(v, into); err != nil {
		return false, err
	}
	return true, nil
}

// Store will store an agent config in the agents ConfigMap for the given namespace. It will
// also update the current snapshot if the updateSnapshot is true. This update will prevent
// the rollout that otherwise occur when the ConfigMap is updated.
func (c *configWatcher) Store(ctx context.Context, ac *agentconfig.Sidecar, updateSnapshot bool) error {
	bf := bytes.Buffer{}
	if err := yaml.NewEncoder(&bf).Encode(ac); err != nil {
		return err
	}
	yml := bf.String()

	create := false
	ns := ac.Namespace
	api := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(ns)
	cm, err := api.Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("unable to get ConfigMap %s: %w", agentconfig.ConfigMap, err)
		}
		create = true
	}

	eq := false
	c.Lock()
	nm, ok := c.data[ns]
	if ok {
		if old, ok := nm[ac.AgentName]; ok {
			eq = old == yml
		}
	} else {
		nm = make(map[string]string)
		c.data[ns] = nm
	}
	if updateSnapshot && !eq {
		nm[ac.AgentName] = yml
	}
	c.Unlock()
	if eq {
		return nil
	}

	if create {
		cm = &core.ConfigMap{
			TypeMeta: meta.TypeMeta{
				Kind:       "ConfigMap",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      agentconfig.ConfigMap,
				Namespace: ns,
			},
			Data: map[string]string{
				ac.AgentName: yml,
			},
		}
		dlog.Infof(ctx, "creating new ConfigMap %s.%s", agentconfig.ConfigMap, ns)
		_, err = api.Create(ctx, cm, meta.CreateOptions{})
	} else {
		dlog.Infof(ctx, "updating ConfigMap %s.%s", agentconfig.ConfigMap, ns)
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		cm.Data[ac.AgentName] = yml
		_, err = api.Update(ctx, cm, meta.UpdateOptions{})
	}
	return err
}

func (c *configWatcher) Start(ctx context.Context) (modCh <-chan entry, delCh <-chan entry, err error) {
	c.Lock()
	c.modCh = make(chan entry)
	c.delCh = make(chan entry)
	c.Unlock()

	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	do := func(ns string) {
		dlog.Infof(ctx, "Started watcher for ConfigMap %s.%s", agentconfig.ConfigMap, ns)
		defer dlog.Infof(ctx, "Ended watcher for ConfigMap %s.%s", agentconfig.ConfigMap, ns)

		// The Watch will perform a http GET call to the kubernetes API server, and that connection will not remain open forever
		// so when it closes, the watch must start over. This goes on until the context is cancelled.
		for ctx.Err() == nil {
			w, err := api.ConfigMaps(ns).Watch(ctx, meta.SingleObject(meta.ObjectMeta{
				Name: agentconfig.ConfigMap,
			}))
			if err != nil {
				dlog.Errorf(ctx, "unable to create watcher: %v", err)
				return
			}
			if !c.eventHandler(ctx, w.ResultChan()) {
				return
			}
		}
	}

	if len(c.namespaces) == 0 {
		go do("")
	} else {
		for _, ns := range c.namespaces {
			go do(ns)
		}
	}
	return c.modCh, c.delCh, nil
}

func (c *configWatcher) eventHandler(ctx context.Context, evCh <-chan watch.Event) bool {
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
					dlog.Infof(ctx, "%s %s.%s", event.Type, m.Name, m.Namespace)
					c.update(ctx, m.Namespace, nil)
				}
			case watch.Added, watch.Modified:
				if m, ok := event.Object.(*core.ConfigMap); ok {
					dlog.Infof(ctx, "%s %s.%s", event.Type, m.Name, m.Namespace)
					if m.Name != agentconfig.ConfigMap {
						continue
					}
					c.update(ctx, m.Namespace, m.Data)
				}
			}
		}
	}
}

func writeToChan(ctx context.Context, es []entry, ch chan<- entry) {
	for _, e := range es {
		if e.name == agentconfig.InjectorKey {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case ch <- e:
		}
	}
}

func (c *configWatcher) update(ctx context.Context, ns string, m map[string]string) {
	var dels []entry
	c.Lock()
	data, ok := c.data[ns]
	if !ok {
		data = make(map[string]string, len(m))
		c.data[ns] = data
	}
	for k, v := range data {
		if _, ok := m[k]; !ok {
			delete(data, k)
			dels = append(dels, entry{name: k, namespace: ns, value: v})
		}
	}
	var mods []entry
	for k, v := range m {
		if ov, ok := data[k]; !ok || ov != v {
			mods = append(mods, entry{name: k, namespace: ns, value: v})
			data[k] = v
		}
	}
	c.Unlock()
	go writeToChan(ctx, dels, c.delCh)
	go writeToChan(ctx, mods, c.modCh)
}

func (c *configWatcher) DeleteMapsAndRolloutAll(ctx context.Context) {
	c.cancel() // No more updates from watcher
	c.RLock()
	defer c.RUnlock()

	now := meta.NewDeleteOptions(0)
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	for ns, wlm := range c.data {
		for k, v := range wlm {
			if k == agentconfig.InjectorKey {
				continue
			}
			e := &entry{name: k, namespace: ns, value: v}
			ac, wl, err := e.workload(ctx)
			if err != nil {
				dlog.Errorf(ctx, "unable to get workload for %s.%s %s: %v", k, ns, v, err)
				continue
			}
			if ac.Create {
				// Deleted before it was generated, just ignore
				continue
			}
			triggerRollout(ctx, wl)
		}
		if err := api.ConfigMaps(ns).Delete(ctx, agentconfig.ConfigMap, *now); err != nil {
			dlog.Errorf(ctx, "unable to delete ConfigMap %s-%s: %v", agentconfig.ConfigMap, ns, err)
		}
	}
}

// UninstallV25 will undo changes that telepresence versions prior to 2.6.0 did to workloads and
// also add an initial entry in the agents ConfigMap for all workloads that had an agent or
// was annotated to inject an agent.
func (c *configWatcher) UninstallV25(ctx context.Context) {
	var affectedWorkloads []k8sapi.Workload
	if len(c.namespaces) == 0 {
		affectedWorkloads = v25uninstall.RemoveAgents(ctx, "")
	} else {
		for _, ns := range c.namespaces {
			affectedWorkloads = append(affectedWorkloads, v25uninstall.RemoveAgents(ctx, ns)...)
		}
	}
	for _, wl := range affectedWorkloads {
		ac, err := agentmap.Generate(ctx, wl)
		if err == nil {
			err = c.Store(ctx, ac, false)
		}
		if err != nil {
			dlog.Warn(ctx, err)
		}
	}
}

package mutator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/mutator/v25uninstall"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/install"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type Map interface {
	GetInto(string, string, any) (bool, error)
	Run(context.Context) error
	Delete(context.Context, string, string) error
	Store(context.Context, *agentconfig.Sidecar, bool) error
	DeleteMapsAndRolloutAll(ctx context.Context)
	UninstallV25(ctx context.Context)
}

func decode(v string, into any) error {
	return yaml.Unmarshal([]byte(v), into)
}

func Load(ctx context.Context, namespace string) (m Map, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		if err != nil {
			cancel()
		}
	}()

	env := managerutil.GetEnv(ctx)
	ns := env.GetManagedNamespaces()
	dlog.Infof(ctx, "Loading ConfigMaps from %v", ns)
	return NewWatcher(agentconfig.ConfigMap, ns...), nil
}

func (e *entry) workload(ctx context.Context) (*agentconfig.Sidecar, k8sapi.Workload, error) {
	ac := &agentconfig.Sidecar{}
	if err := decode(e.value, ac); err != nil {
		return nil, nil, fmt.Errorf("failed to decode ConfigMap entry %q into an agent config", e.value)
	}
	wl, err := k8sapi.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
	if err != nil {
		return nil, nil, err
	}
	return ac, wl, nil
}

func triggerRollout(ctx context.Context, wl k8sapi.Workload) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "mutator.triggerRollout")
	defer span.End()
	k8sapi.RecordWorkloadInfo(span, wl)
	if rs, ok := k8sapi.ReplicaSetImpl(wl); ok {
		// Rollout of a replicatset will not recreate the pods. In order for that to happen, the
		// set must be scaled down and then up again.
		dlog.Debugf(ctx, "Performing ReplicaSet rollout of %s.%s using scaling", wl.GetName(), wl.GetNamespace())
		replicas := 1
		if rp := rs.Spec.Replicas; rp != nil {
			replicas = int(*rp)
		}
		if replicas > 0 {
			patch := `{"spec": {"replicas": 0}}`
			if err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(patch)); err != nil {
				err = fmt.Errorf("unable to scale ReplicaSet %s.%s to zero: %w", wl.GetName(), wl.GetNamespace(), err)
				dlog.Error(ctx, err)
				span.SetStatus(codes.Error, err.Error())
				return
			}
			patch = fmt.Sprintf(`{"spec": {"replicas": %d}}`, replicas)
			if err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(patch)); err != nil {
				err = fmt.Errorf("unable to scale ReplicaSet %s.%s to %d: %v", wl.GetName(), wl.GetNamespace(), replicas, err)
				dlog.Error(ctx, err)
				span.SetStatus(codes.Error, err.Error())
			}
		} else {
			span.AddEvent("tel2.noop-rollout")
			dlog.Debugf(ctx, "ReplicaSet %s.%s has zero replicas so rollout was a no-op", wl.GetName(), wl.GetNamespace())
		}
		return
	}
	restartAnnotation := fmt.Sprintf(
		`{"spec": {"template": {"metadata": {"annotations": {"%srestartedAt": "%s"}}}}}`,
		install.DomainPrefix,
		time.Now().Format(time.RFC3339),
	)
	span.AddEvent("tel2.do-rollout")
	if err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(restartAnnotation)); err != nil {
		err = fmt.Errorf("unable to patch %s %s.%s: %v", wl.GetKind(), wl.GetName(), wl.GetNamespace(), err)
		dlog.Error(ctx, err)
		span.SetStatus(codes.Error, err.Error())
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
	link      trace.Link
}

func (c *configWatcher) Run(ctx context.Context) error {
	ctx, c.cancel = context.WithCancel(ctx)
	addCh, delCh, err := c.Start(ctx)
	if err != nil {
		return err
	}
	var agentImage string
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-delCh:
			c.handleDelete(ctx, e)
		case e := <-addCh:
			c.handleAdd(ctx, e, agentImage)
		}
	}
}

func (c *configWatcher) handleAdd(ctx context.Context, e entry, agentImage string) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "mutator.handleAdd",
		trace.WithNewRoot(),
		trace.WithLinks(e.link),
	)
	defer span.End()
	dlog.Debugf(ctx, "add %s.%s", e.name, e.namespace)
	ac, wl, err := e.workload(ctx)
	ac.RecordInSpan(span)
	k8sapi.RecordWorkloadInfo(span, wl)
	if err != nil {
		if !errors.IsNotFound(err) {
			dlog.Error(ctx, err)
		}
		return
	}
	if ac.Manual {
		span.SetAttributes(attribute.Bool("tel2.manual", ac.Manual))
		// Manually added, just ignore
		return
	}
	if ac.Create {
		if agentImage == "" {
			agentImage = managerutil.GetAgentImage(ctx)
		}
		if ac, err = agentmap.Generate(ctx, wl, managerutil.GetEnv(ctx).GeneratorConfig(agentImage)); err != nil {
			dlog.Error(ctx, err)
		} else if err = c.Store(ctx, ac, false); err != nil { // Calling Store() will generate a new event, so we skip rollout here
			dlog.Error(ctx, err)
		}
		return
	}
	triggerRollout(ctx, wl)
}

func (*configWatcher) handleDelete(ctx context.Context, e entry) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "mutator.handleAdd",
		trace.WithNewRoot(),
		trace.WithLinks(e.link),
	)
	defer span.End()
	dlog.Debugf(ctx, "del %s.%s", e.name, e.namespace)
	ac, wl, err := e.workload(ctx)
	k8sapi.RecordWorkloadInfo(span, wl)
	ac.RecordInSpan(span)
	if err != nil {
		if !errors.IsNotFound(err) {
			dlog.Error(ctx, err)
		}
		return
	}
	if ac.Create || ac.Manual {
		// Deleted before it was generated or manually added, just ignore
		return
	}
	triggerRollout(ctx, wl)
}

func (c *configWatcher) GetInto(key, ns string, into any) (bool, error) {
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

// Delete will delete an agent config from the agents ConfigMap for the given namespace. It will
// also update the current snapshot.
// An attempt to delete a manually added config is a no-op
func (c *configWatcher) Delete(ctx context.Context, name, namespace string) error {
	api := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(namespace)
	cm, err := api.Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("unable to get ConfigMap %s: %w", agentconfig.ConfigMap, err)
		}
		return nil
	}
	yml, ok := cm.Data[name]
	if !ok {
		return nil
	}
	var ac agentconfig.Sidecar
	if err = decode(yml, &ac); err != nil {
		return err
	}
	if ac.Manual {
		return nil
	}
	delete(cm.Data, name)
	dlog.Debugf(ctx, "Deleting %s from ConfigMap %s.%s", name, agentconfig.ConfigMap, namespace)
	_, err = api.Update(ctx, cm, meta.UpdateOptions{})
	return err
}

// Store will store an agent config in the agents ConfigMap for the given namespace. It will
// also update the current snapshot if the updateSnapshot is true. This update will prevent
// the rollout that otherwise occur when the ConfigMap is updated.
func (c *configWatcher) Store(ctx context.Context, ac *agentconfig.Sidecar, updateSnapshot bool) error {
	js, err := yaml.Marshal(ac)
	if err != nil {
		return err
	}

	yml := string(js)
	ns := ac.Namespace
	c.RLock()
	var eq bool
	if nm, ok := c.data[ns]; ok {
		eq = nm[ac.AgentName] == yml
	}
	c.RUnlock()
	if eq {
		return nil
	}

	create := false
	api := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(ns)
	cm, err := api.Get(ctx, agentconfig.ConfigMap, meta.GetOptions{})
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("unable to get ConfigMap %s: %w", agentconfig.ConfigMap, err)
		}
		create = true
	} else {
		// Ensure that we're not about to overwrite a manually added config entry
		if currentYml, ok := cm.Data[ac.AgentName]; ok {
			var currAc agentconfig.Sidecar
			if err = decode(currentYml, &currAc); err == nil && currAc.Manual {
				dlog.Warnf(ctx, "avoided an attempt to overwrite manually added config entry for %s.%s", ac.AgentName, ns)
				return nil
			}
		}
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		}
		if cm.Data[ac.AgentName] == yml {
			// Race condition. Snapshot isn't updated yet, or we wouldn't have gotten here.
			return nil
		}
	}

	if updateSnapshot {
		c.Lock()
		if nm, ok := c.data[ns]; ok {
			nm[ac.AgentName] = yml
		}
		c.Unlock()
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
		dlog.Debugf(ctx, "Creating new ConfigMap %s.%s with %s", agentconfig.ConfigMap, ns, ac.AgentName)
		_, err = api.Create(ctx, cm, meta.CreateOptions{})
	} else {
		if _, ok := cm.Data[ac.AgentName]; ok {
			dlog.Debugf(ctx, "Updating %s in ConfigMap %s.%s", ac.AgentName, agentconfig.ConfigMap, ns)
		} else {
			dlog.Debugf(ctx, "Adding %s to ConfigMap %s.%s", ac.AgentName, agentconfig.ConfigMap, ns)
		}
		cm.Data[ac.AgentName] = yml
		_, err = api.Update(ctx, cm, meta.UpdateOptions{})
	}
	return err
}

func whereWeWatch(ns string) string {
	if ns == "" {
		return "cluster wide"
	}
	return "in namespace " + ns
}

func (c *configWatcher) watchConfigMap(ctx context.Context, ns string) {
	dlog.Infof(ctx, "Started watcher for ConfigMap %s %s", agentconfig.ConfigMap, whereWeWatch(ns))
	defer dlog.Infof(ctx, "Ended watcher for ConfigMap %s %s", agentconfig.ConfigMap, whereWeWatch(ns))

	// The Watch will perform a http GET call to the kubernetes API server, and that connection will not remain open forever
	// so when it closes, the watch must start over. This goes on until the context is cancelled.
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	for ctx.Err() == nil {
		w, err := api.ConfigMaps(ns).Watch(ctx, meta.SingleObject(meta.ObjectMeta{
			Name: agentconfig.ConfigMap,
		}))
		if err != nil {
			dlog.Errorf(ctx, "unable to create configmap watcher: %v", err)
			return
		}
		if !c.configMapEventHandler(ctx, w.ResultChan()) {
			return
		}
	}
}

func (c *configWatcher) watchServices(ctx context.Context, ns string) {
	dlog.Infof(ctx, "Started watcher for Services %s", whereWeWatch(ns))
	defer dlog.Infof(ctx, "Ended watcher for Services %s", whereWeWatch(ns))

	// The Watch will perform a http GET call to the kubernetes API server, and that connection will not remain open forever
	// so when it closes, the watch must start over. This goes on until the context is cancelled.
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	for ctx.Err() == nil {
		w, err := api.Services(ns).Watch(ctx, meta.ListOptions{})
		if err != nil {
			dlog.Errorf(ctx, "unable to create service watcher: %v", err)
			return
		}
		if !c.svcEventHandler(ctx, w.ResultChan()) {
			return
		}
	}
}

func (c *configWatcher) Start(ctx context.Context) (modCh <-chan entry, delCh <-chan entry, err error) {
	c.Lock()
	c.modCh = make(chan entry)
	c.delCh = make(chan entry)
	c.Unlock()
	if len(c.namespaces) == 0 {
		go c.watchConfigMap(ctx, "")
		go c.watchServices(ctx, "")
	} else {
		for _, ns := range c.namespaces {
			go c.watchConfigMap(ctx, ns)
			go c.watchServices(ctx, ns)
		}
	}
	return c.modCh, c.delCh, nil
}

func (c *configWatcher) configMapEventHandler(ctx context.Context, evCh <-chan watch.Event) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case event, ok := <-evCh:
			{
				if !ok {
					return true // restart watcher
				}
				ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "mutator.configMapEventHandler",
					trace.WithNewRoot(), // Because the watcher is long lived, if we put these spans under it there's a high chance they don't get collected.
					trace.WithAttributes(
						attribute.String("tel2.event-type", string(event.Type)),
					))
				switch event.Type {
				case watch.Deleted:
					if m, ok := event.Object.(*core.ConfigMap); ok {
						span.SetAttributes(
							attribute.String("tel2.cm-name", m.Name),
							attribute.String("tel2.cm-namespace", m.Namespace),
						)
						dlog.Debugf(ctx, "%s %s.%s", event.Type, m.Name, m.Namespace)
						c.update(ctx, m.Namespace, nil)
					}
				case watch.Added, watch.Modified:
					if m, ok := event.Object.(*core.ConfigMap); ok {
						span.SetAttributes(
							attribute.String("tel2.cm-name", m.Name),
							attribute.String("tel2.cm-namespace", m.Namespace),
						)
						dlog.Debugf(ctx, "%s %s.%s", event.Type, m.Name, m.Namespace)
						if m.Name != agentconfig.ConfigMap {
							continue
						}
						c.update(ctx, m.Namespace, m.Data)
					}
				}
				span.End()
			}
		}
	}
}

func (c *configWatcher) configsAffectedBySvcUID(ctx context.Context, nsData map[string]string, uid types.UID) []*agentconfig.Sidecar {
	references := func(ac *agentconfig.Sidecar, uid types.UID) bool {
		for _, cn := range ac.Containers {
			for _, ic := range cn.Intercepts {
				if ic.ServiceUID == uid {
					return true
				}
			}
		}
		return false
	}

	var affected []*agentconfig.Sidecar
	for _, cfg := range nsData {
		ac := &agentconfig.Sidecar{}
		if err := decode(cfg, ac); err != nil {
			dlog.Errorf(ctx, "failed to decode ConfigMap entry %q into an agent config", cfg)
		} else if references(ac, uid) {
			affected = append(affected, ac)
		}
	}
	return affected
}

func (c *configWatcher) configsAffectedByWorkloads(ctx context.Context, nsData map[string]string, wls []k8sapi.Workload) []*agentconfig.Sidecar {
	var affected []*agentconfig.Sidecar
	for _, wl := range wls {
		if nsd, ok := nsData[wl.GetName()]; ok {
			ac := &agentconfig.Sidecar{}
			if err := decode(nsd, ac); err != nil {
				dlog.Errorf(ctx, "failed to decode ConfigMap entry %q into an agent config", nsd)
			} else {
				affected = append(affected, ac)
			}
		}
	}
	return affected
}

func (c *configWatcher) affectedConfigs(ctx context.Context, svc *core.Service, isDelete bool) []*agentconfig.Sidecar {
	c.RLock()
	defer c.RUnlock()
	ns := svc.Namespace
	nsData, ok := c.data[ns]
	if !ok || len(nsData) == 0 {
		return nil
	}

	if isDelete {
		return c.configsAffectedBySvcUID(ctx, nsData, svc.UID)
	}

	var wls []k8sapi.Workload
	// Find workloads that the updated service is referencing.
	selector := svc.Spec.Selector
	if len(selector) > 0 {
		if deps, err := k8sapi.Deployments(ctx, ns, selector); err == nil {
			wls = append(wls, deps...)
		}
		if reps, err := k8sapi.ReplicaSets(ctx, ns, selector); err == nil {
			wls = append(wls, reps...)
		}
		if stss, err := k8sapi.StatefulSets(ctx, ns, selector); err == nil {
			wls = append(wls, stss...)
		}
	}
	return c.configsAffectedByWorkloads(ctx, nsData, wls)
}

func (c *configWatcher) updateSvc(ctx context.Context, svc *core.Service, isDelete bool) {
	// Does the snapshot contain workloads that we didn't find using the service's Spec.Selector?
	// If so, include them, or if workload for the config entry isn't found, delete that entry
	cfg := managerutil.GetEnv(ctx).GeneratorConfig(managerutil.GetAgentImage(ctx))
	for _, ac := range c.affectedConfigs(ctx, svc, isDelete) {
		wl, err := k8sapi.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
		if err != nil {
			if errors.IsNotFound(err) {
				dlog.Debugf(ctx, "Deleting config entry for %s %s.%s", ac.WorkloadKind, ac.WorkloadName, ac.Namespace)
				if err = c.Delete(ctx, ac.AgentName, ac.Namespace); err != nil {
					dlog.Error(ctx, err)
				}
			} else {
				dlog.Error(ctx, err)
			}
			continue
		}
		dlog.Debugf(ctx, "Regenerating config entry for %s %s.%s", ac.WorkloadKind, ac.WorkloadName, ac.Namespace)
		acn, err := agentmap.Generate(ctx, wl, cfg)
		if err != nil {
			dlog.Error(ctx, err)
			continue
		}
		if err = c.Store(ctx, acn, false); err != nil {
			dlog.Error(ctx, err)
		}
	}
}

func (c *configWatcher) svcEventHandler(ctx context.Context, evCh <-chan watch.Event) bool {
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
				if svc, ok := event.Object.(*core.Service); ok {
					c.updateSvc(ctx, svc, true)
				}
			case watch.Added, watch.Modified:
				if svc, ok := event.Object.(*core.Service); ok {
					c.updateSvc(ctx, svc, false)
				}
			}
		}
	}
}

func writeToChan(ctx context.Context, es []entry, ch chan<- entry) {
	for _, e := range es {
		select {
		case <-ctx.Done():
			return
		case ch <- e:
		}
	}
}

func (c *configWatcher) update(ctx context.Context, ns string, m map[string]string) {
	span := trace.SpanFromContext(ctx)
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
			dels = append(dels, entry{name: k, namespace: ns, value: v, link: trace.LinkFromContext(ctx)})
			span.AddEvent("tel2.cm-delete", trace.WithAttributes(
				attribute.String("tel2.workload-name", k),
				attribute.String("tel2.workload-namespace", ns),
			))
		}
	}
	var mods []entry
	for k, v := range m {
		if ov, ok := data[k]; !ok || ov != v {
			mods = append(mods, entry{name: k, namespace: ns, value: v, link: trace.LinkFromContext(ctx)})
			data[k] = v
			span.AddEvent("tel2.cm-mod", trace.WithAttributes(
				attribute.String("tel2.workload-name", k),
				attribute.String("tel2.workload-namespace", ns),
			))
		}
	}
	c.Unlock()
	if len(dels) > 0 {
		go writeToChan(ctx, dels, c.delCh)
	}
	if len(mods) > 0 {
		go writeToChan(ctx, mods, c.modCh)
	}
}

func (c *configWatcher) DeleteMapsAndRolloutAll(ctx context.Context) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "mutator.DeleteMapsAndRolloutAll")
	defer span.End()
	c.cancel() // No more updates from watcher
	c.RLock()
	defer c.RUnlock()

	now := meta.NewDeleteOptions(0)
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	for ns, wlm := range c.data {
		for k, v := range wlm {
			e := &entry{name: k, namespace: ns, value: v, link: trace.LinkFromContext(ctx)}
			ac, wl, err := e.workload(ctx)
			if err != nil {
				if !errors.IsNotFound(err) {
					dlog.Errorf(ctx, "unable to get workload for %s.%s %s: %v", k, ns, v, err)
				}
				continue
			}
			if ac.Create || ac.Manual {
				// Deleted before it was generated or manually added, just ignore
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
	gc := managerutil.GetEnv(ctx).GeneratorConfig(managerutil.GetAgentImage(ctx))
	for _, wl := range affectedWorkloads {
		ac, err := agentmap.Generate(ctx, wl, gc)
		if err == nil {
			err = c.Store(ctx, ac, false)
		}
		if err != nil {
			dlog.Warn(ctx, err)
		}
	}
}

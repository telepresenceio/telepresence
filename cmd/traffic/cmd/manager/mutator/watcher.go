package mutator

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

type Map interface {
	Get(string, string) (agentconfig.SidecarExt, error)
	Run(context.Context) error
	OnAdd(context.Context, k8sapi.Workload, agentconfig.SidecarExt) error
	OnDelete(context.Context, k8sapi.Workload) error
	DeleteMapsAndRolloutAll(ctx context.Context)

	store(ctx context.Context, acx agentconfig.SidecarExt, updateSnapshot bool) error
	remove(ctx context.Context, name, namespace string) error
}

var NewWatcherFunc = NewWatcher //nolint:gochecknoglobals // extension point

func Load(ctx context.Context) (m Map, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer func() {
		if err != nil {
			cancel()
		}
	}()

	env := managerutil.GetEnv(ctx)
	ns := env.ManagedNamespaces
	dlog.Infof(ctx, "Loading ConfigMaps from %v", ns)
	return NewWatcherFunc(agentconfig.ConfigMap, ns...), nil
}

func (e *entry) workload(ctx context.Context) (agentconfig.SidecarExt, k8sapi.Workload, error) {
	scx, err := agentconfig.UnmarshalYAML([]byte(e.value))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode ConfigMap entry %q into an agent config", e.value)
	}
	ac := scx.AgentConfig()
	wl, err := tracing.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
	if err != nil {
		return nil, nil, err
	}
	return ac, wl, nil
}

func agentContainer(pod *core.Pod) *core.Container {
	cns := pod.Spec.Containers
	for i := range cns {
		cn := &cns[i]
		if cn.Name == agentconfig.ContainerName {
			return cn
		}
	}
	return nil
}

func isPodRunning(pod *core.Pod) bool {
	for _, cn := range pod.Status.ContainerStatuses {
		if r := cn.State.Running; r != nil && !r.StartedAt.IsZero() {
			// At least one container is running.
			return true
		}
	}
	return false
}

// isRolloutNeeded checks if the agent's entry in telepresence-agents matches the actual state of the
// pods. If it does, then there's no reason to trigger a rollout.
func isRolloutNeeded(ctx context.Context, wl k8sapi.Workload, ac *agentconfig.Sidecar) bool {
	podLabels := wl.GetPodTemplate().GetObjectMeta().GetLabels()
	if len(podLabels) == 0 {
		// Have never seen this, but if it happens, then rollout only if an agent is desired
		dlog.Debugf(ctx, "Rollout of %s.%s is necessary. Pod template has no pod labels",
			wl.GetName(), wl.GetNamespace())
		return true
	}

	pods, err := k8sapi.Pods(ctx, wl.GetNamespace(), podLabels)
	if err != nil {
		dlog.Debugf(ctx, "Rollout of %s.%s is necessary. Unable to retrieve current pods: %v",
			wl.GetName(), wl.GetNamespace(), err)
		return true
	}

	runningPods := 0
	for _, podObj := range pods {
		pod, ok := k8sapi.PodImpl(podObj)
		if !(ok && isPodRunning(pod)) {
			continue
		}
		runningPods++

		podAc := agentContainer(pod)
		if ac == nil {
			if podAc == nil {
				continue
			}
			dlog.Debugf(ctx, "Rollout of %s.%s is necessary. No agent is desired but the pod %s has one",
				wl.GetName(), wl.GetNamespace(), pod.GetName())
			return true
		}
		if podAc == nil {
			// Rollout because an agent is desired but the pod doesn't have one
			dlog.Debugf(ctx, "Rollout of %s.%s is necessary. An agent is desired but the pod %s doesn't have one",
				wl.GetName(), wl.GetNamespace(), pod.GetName())
			return true
		}
		desiredAc := agentconfig.AgentContainer(ctx, pod, ac)
		if !containerEqual(podAc, desiredAc) {
			dlog.Debugf(ctx, "Rollout of %s.%s is necessary. The desired agent is not equal to the existing agent in pod %s",
				wl.GetName(), wl.GetNamespace(), pod.GetName())
			return true
		}
		for _, cn := range ac.Containers {
			var found *core.Container
			cns := pod.Spec.Containers
			for i := range cns {
				if cns[i].Name == cn.Name {
					found = &cns[i]
					break
				}
			}
			if found == nil {
				dlog.Debugf(ctx, "Rollout of %s.%s is necessary. The desired pod should contain container %s",
					wl.GetName(), wl.GetNamespace(), cn.Name)
				return true
			}
			if cn.Replace {
				// Ensure that the replaced container is disabled
				if !(found.Image == sleeperImage && slices.Equal(found.Args, sleeperArgs)) {
					dlog.Debugf(ctx, "Rollout of %s.%s is necessary. The desired pod's container %s should be disabled",
						wl.GetName(), wl.GetNamespace(), cn.Name)
					return true
				}
			} else {
				// Ensure that the replaced container is not disabled
				if found.Image == sleeperImage && slices.Equal(found.Args, sleeperArgs) {
					dlog.Debugf(ctx, "Rollout of %s.%s is necessary. The desired pod's container %s should not be disabled",
						wl.GetName(), wl.GetNamespace(), cn.Name)
					return true
				}
			}
		}
	}
	// Rollout if there are no running pods
	if runningPods == 0 && ac != nil {
		dlog.Debugf(ctx, "Rollout of %s.%s is necessary. An agent is desired and there are no pods",
			wl.GetName(), wl.GetNamespace())
		return true
	}
	dlog.Debugf(ctx, "Rollout of %s.%s is not necessary. All pods have the desired agent state",
		wl.GetName(), wl.GetNamespace())
	return false
}

func triggerRollout(ctx context.Context, wl k8sapi.Workload, ac *agentconfig.Sidecar) {
	if !isRolloutNeeded(ctx, wl, ac) {
		return
	}

	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "mutator.triggerRollout")
	defer span.End()
	tracing.RecordWorkloadInfo(span, wl)

	if rs, ok := k8sapi.ReplicaSetImpl(wl); ok {
		triggerRolloutReplicaSet(ctx, wl, rs, span)
		return
	}
	restartAnnotation := fmt.Sprintf(
		`{"spec": {"template": {"metadata": {"annotations": {"%srestartedAt": "%s"}}}}}`,
		DomainPrefix,
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

func triggerRolloutReplicaSet(ctx context.Context, wl k8sapi.Workload, rs *v1.ReplicaSet, span trace.Span) {
	// Rollout of a replicatset will not recreate the pods. In order for that to happen, the
	// set must be scaled down and then up again.
	dlog.Debugf(ctx, "Performing ReplicaSet rollout of %s.%s using scaling", wl.GetName(), wl.GetNamespace())
	replicas := int32(1)
	if rp := rs.Spec.Replicas; rp != nil {
		replicas = *rp
	}
	if replicas == 0 {
		span.AddEvent("tel2.noop-rollout")
		dlog.Debugf(ctx, "ReplicaSet %s.%s has zero replicas so rollout was a no-op", wl.GetName(), wl.GetNamespace())
		return
	}

	waitForReplicaCount := func(count int32) error {
		for retry := 0; retry < 200; retry++ {
			if nwl, err := k8sapi.GetReplicaSet(ctx, wl.GetName(), wl.GetNamespace()); err == nil {
				rs, _ = k8sapi.ReplicaSetImpl(nwl)
				if rp := rs.Spec.Replicas; rp != nil && *rp == count {
					wl = nwl
					return nil
				}
			}
			dtime.SleepWithContext(ctx, 300*time.Millisecond)
		}
		return fmt.Errorf("ReplicaSet %s.%s never scaled down to zero", wl.GetName(), wl.GetNamespace())
	}

	patch := `{"spec": {"replicas": 0}}`
	if err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(patch)); err != nil {
		err = fmt.Errorf("unable to scale ReplicaSet %s.%s to zero: %w", wl.GetName(), wl.GetNamespace(), err)
		dlog.Error(ctx, err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	if err := waitForReplicaCount(0); err != nil {
		dlog.Error(ctx, err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	dlog.Debugf(ctx, "ReplicaSet %s.%s was scaled down to zero. Scaling back to %d", wl.GetName(), wl.GetNamespace(), replicas)
	patch = fmt.Sprintf(`{"spec": {"replicas": %d}}`, replicas)
	if err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(patch)); err != nil {
		err = fmt.Errorf("unable to scale ReplicaSet %s.%s to %d: %v", wl.GetName(), wl.GetNamespace(), replicas, err)
		dlog.Error(ctx, err)
		span.SetStatus(codes.Error, err.Error())
	}
	if err := waitForReplicaCount(replicas); err != nil {
		dlog.Error(ctx, err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
}

// RegenerateAgentMaps load the telepresence-agents config map, regenerates all entries in it,
// and then, if any of the entries changed, it updates the map.
func RegenerateAgentMaps(ctx context.Context, agentImage string) error {
	gc, err := agentmap.GeneratorConfigFunc(agentImage)
	if err != nil {
		return err
	}
	nss := managerutil.GetEnv(ctx).ManagedNamespaces
	if len(nss) == 0 {
		return regenerateAgentMaps(ctx, "", gc)
	}
	for _, ns := range nss {
		if err = regenerateAgentMaps(ctx, ns, gc); err != nil {
			return err
		}
	}
	return nil
}

// regenerateAgentMaps load the telepresence-agents config map, regenerates all entries in it,
// and then, if any of the entries changed, it updates the map.
func regenerateAgentMaps(ctx context.Context, ns string, gc agentmap.GeneratorConfig) error {
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	cml, err := api.ConfigMaps(ns).List(ctx, meta.SingleObject(meta.ObjectMeta{
		Name: agentconfig.ConfigMap,
	}))
	if err != nil {
		return err
	}
	cms := cml.Items
	for i := range cms {
		cm := &cms[i]
		changed := false
		ns := cm.Namespace
		for n, d := range cm.Data {
			e := &entry{name: n, namespace: ns, value: d}
			acx, wl, err := e.workload(ctx)
			if err != nil {
				if !errors.IsNotFound(err) {
					return err
				}
				delete(cm.Data, n) // Workload no longer exists
				changed = true
				continue
			}
			ncx, err := gc.Generate(ctx, wl, acx)
			if err != nil {
				return err
			}
			if cmp.Equal(acx, ncx) {
				continue
			}
			yml, err := ncx.Marshal()
			if err != nil {
				return err
			}
			cm.Data[n] = string(yml)
			changed = true
		}
		if changed {
			_, err = api.ConfigMaps(ns).Update(ctx, cm, meta.UpdateOptions{})
		}
	}
	return err
}

func NewWatcher(name string, namespaces ...string) Map {
	w := &configWatcher{
		name:           name,
		namespaces:     namespaces,
		data:           make(map[string]map[string]string),
		configUpdaters: make(map[string]*configUpdater),
	}
	w.self = w
	return w
}

type configWatcher struct {
	sync.RWMutex
	cancel     context.CancelFunc
	name       string
	namespaces []string
	data       map[string]map[string]string
	modCh      chan entry
	delCh      chan entry

	configUpdatersLock sync.RWMutex
	configUpdaters     map[string]*configUpdater

	self Map // For extension
}

type entry struct {
	name      string
	namespace string
	value     string
	link      trace.Link
}

func (c *configWatcher) SetSelf(self Map) {
	c.self = self
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
			c.handleDelete(ctx, e)
		case e := <-addCh:
			c.handleAdd(ctx, e)
		}
	}
}

func (c *configWatcher) OnAdd(ctx context.Context, wl k8sapi.Workload, acx agentconfig.SidecarExt) error {
	triggerRollout(ctx, wl, acx.AgentConfig())
	return nil
}

func (c *configWatcher) OnDelete(ctx context.Context, wl k8sapi.Workload) error {
	triggerRollout(ctx, wl, nil)
	return nil
}

func (c *configWatcher) handleAdd(ctx context.Context, e entry) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "mutator.handleAdd",
		trace.WithNewRoot(),
		trace.WithLinks(e.link),
	)
	defer span.End()
	dlog.Debugf(ctx, "add %s.%s", e.name, e.namespace)
	scx, wl, err := e.workload(ctx)
	if err != nil {
		if !errors.IsNotFound(err) {
			dlog.Error(ctx, err)
		}
		return
	}
	scx.RecordInSpan(span)
	tracing.RecordWorkloadInfo(span, wl)
	ac := scx.AgentConfig()
	if ac.Manual {
		span.SetAttributes(attribute.Bool("tel2.manual", ac.Manual))
		// Manually added, just ignore
		return
	}
	if ac.Create {
		img := managerutil.GetAgentImage(ctx)
		if img == "" {
			// Unable to get image. This has been logged elsewhere
			return
		}
		gc, err := agentmap.GeneratorConfigFunc(img)
		if err != nil {
			dlog.Error(ctx, err)
			return
		}
		if acx, err := gc.Generate(ctx, wl, ac); err != nil {
			dlog.Error(ctx, err)
		} else if err = c.store(ctx, acx, false); err != nil { // Calling store() will generate a new event, so we skip rollout here
			dlog.Error(ctx, err)
		}
		return
	}
	if err = c.self.OnAdd(ctx, wl, scx); err != nil {
		dlog.Error(ctx, err)
	}
}

func (c *configWatcher) handleDelete(ctx context.Context, e entry) {
	ctx, span := otel.GetTracerProvider().Tracer("").Start(ctx, "mutator.handleAdd",
		trace.WithNewRoot(),
		trace.WithLinks(e.link),
	)
	defer span.End()
	dlog.Debugf(ctx, "del %s.%s", e.name, e.namespace)
	scx, wl, err := e.workload(ctx)
	if err != nil {
		if !errors.IsNotFound(err) {
			dlog.Error(ctx, err)
		}
		return
	}
	tracing.RecordWorkloadInfo(span, wl)
	scx.RecordInSpan(span)
	ac := scx.AgentConfig()
	if ac.Create || ac.Manual {
		// Deleted before it was generated or manually added, just ignore
		return
	}
	if err = c.self.OnDelete(ctx, wl); err != nil {
		dlog.Error(ctx, err)
	}
}

func (c *configWatcher) Get(key, ns string) (agentconfig.SidecarExt, error) {
	c.RLock()
	var v string
	vs, ok := c.data[ns]
	if ok {
		v, ok = vs[key]
	}
	c.RUnlock()
	if !ok {
		return nil, nil
	}
	return agentconfig.UnmarshalYAML([]byte(v))
}

// remove will delete an agent config from the agents ConfigMap for the given namespace. It will
// also update the current snapshot.
// An attempt to delete a manually added config is a no-op.
func (c *configWatcher) remove(ctx context.Context, name, namespace string) error {
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
	scx, err := agentconfig.UnmarshalYAML([]byte(yml))
	if err != nil {
		return err
	}
	if scx.AgentConfig().Manual {
		return nil
	}
	delete(cm.Data, name)
	dlog.Debugf(ctx, "Deleting %s from ConfigMap %s.%s", name, agentconfig.ConfigMap, namespace)
	_, err = api.Update(ctx, cm, meta.UpdateOptions{})
	return err
}

// store an agent config in the agents ConfigMap for the given namespace. It will
// also update the current snapshot if the updateSnapshot is true. This update will prevent
// the rollout that otherwise occur when the ConfigMap is updated.
func (c *configWatcher) store(ctx context.Context, acx agentconfig.SidecarExt, updateSnapshot bool) error {
	js, err := acx.Marshal()
	if err != nil {
		return err
	}

	ac := acx.AgentConfig()
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

	var cu *configUpdater

	newGroup := false

	for {
		// Check if the group is in the pool.
		c.configUpdatersLock.Lock()
		if _, ok := c.configUpdaters[ns]; !ok {
			cu = createConfigUpdater(ctx, c, ns)
			c.configUpdaters[ns] = cu
			newGroup = true
		} else {
			cu = c.configUpdaters[ns]
		}
		c.configUpdatersLock.Unlock()

		cu.Lock()
		// If the config updater already has updated the config map, this flag will be true, so drop it.
		if cu.Updated {
			cu.Unlock()
			continue
		}

		cu.Config[ac.AgentName] = yml

		if updateSnapshot {
			cu.AddToSnapshot(ac.AgentName)
		}

		cu.Unlock()

		break
	}

	if newGroup {
		cu.Go(cu.updateConfigMap)
	}

	return cu.Wait()
}

func createConfigUpdater(ctx context.Context, configWatcher *configWatcher, namespace string) *configUpdater {
	grp, grpCtx := errgroup.WithContext(ctx)
	return &configUpdater{
		Group: grp,
		Mutex: &sync.Mutex{},
		cw:    configWatcher,

		ctx:           grpCtx,
		namespace:     namespace,
		addToSnapshot: map[string]struct{}{},

		Config:  map[string]string{},
		Updated: false,
	}
}

type configUpdater struct {
	*errgroup.Group
	*sync.Mutex

	cw *configWatcher

	ctx           context.Context
	namespace     string
	addToSnapshot map[string]struct{}

	Config  map[string]string
	Updated bool
}

func (c *configUpdater) AddToSnapshot(agentName string) {
	c.addToSnapshot[agentName] = struct{}{}
}

func (c *configUpdater) updateConfigMap() error {
	// Any other update for this namespace will have to start a new group at this point.
	defer func() {
		c.cw.configUpdatersLock.Lock()
		delete(c.cw.configUpdaters, c.namespace)
		c.cw.configUpdatersLock.Unlock()
	}()

	api := k8sapi.GetK8sInterface(c.ctx).CoreV1().ConfigMaps(c.namespace)

	create := false

	cm, err := api.Get(c.ctx, agentconfig.ConfigMap, meta.GetOptions{})
	if err != nil {
		if !errors.IsNotFound(err) {
			return fmt.Errorf("unable to get ConfigMap %s: %w", agentconfig.ConfigMap, err)
		}
		create = true
	}

	if create {
		cm = &core.ConfigMap{
			TypeMeta: meta.TypeMeta{
				Kind:       "ConfigMap",
				APIVersion: "v1",
			},
			ObjectMeta: meta.ObjectMeta{
				Name:      agentconfig.ConfigMap,
				Namespace: c.namespace,
			},
		}
	}

	var cmData map[string]string

	c.Lock() // Lock the config updater to avoid any addition to c.Config.
	defer func() {
		c.Updated = true
		c.Unlock()
	}()

	cmData = maps.Copy(c.cw.data[c.namespace])

	for agentName, yml := range c.Config {
		// Ensure that we're not about to overwrite a manually added config entry
		scx, err := agentconfig.UnmarshalYAML([]byte(yml))
		if err == nil && scx.AgentConfig().Manual {
			dlog.Warnf(c.ctx, "avoided an attempt to overwrite manually added Config entry for %s.%s", agentName, c.namespace)
			continue
		}

		// Race condition. Snapshot isn't updated yet, or we wouldn't have gotten here.
		if cm.Data[agentName] == yml {
			continue
		}

		cmData[agentName] = yml

		if _, updateSnapshotOK := c.addToSnapshot[agentName]; updateSnapshotOK {
			c.cw.Lock()
			nm, ok := c.cw.data[c.namespace]
			if !ok {
				c.cw.data[c.namespace] = make(map[string]string, len(cmData))
				nm = c.cw.data[c.namespace]
			}
			nm[agentName] = yml
			c.cw.Unlock()
		}
	}

	cm.Data = cmData

	if create {
		dlog.Debugf(c.ctx, "Creating new ConfigMap %s.%s", agentconfig.ConfigMap, c.namespace)
		_, err = api.Create(c.ctx, cm, meta.CreateOptions{})
	} else {
		dlog.Debugf(c.ctx, "Updating ConfigMap %s.%s", agentconfig.ConfigMap, c.namespace)
		_, err = api.Update(c.ctx, cm, meta.UpdateOptions{})
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
		c.configMapEventHandler(ctx, w.ResultChan())
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
		c.svcEventHandler(ctx, w.ResultChan())
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

func (c *configWatcher) configMapEventHandler(ctx context.Context, evCh <-chan watch.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-evCh:
			{
				if !ok {
					return // restart watcher
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

func (c *configWatcher) configsAffectedBySvcUID(ctx context.Context, nsData map[string]string, uid types.UID) []agentconfig.SidecarExt {
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

	var affected []agentconfig.SidecarExt
	for _, cfg := range nsData {
		scx, err := agentconfig.UnmarshalYAML([]byte(cfg))
		if err != nil {
			dlog.Errorf(ctx, "failed to decode ConfigMap entry %q into an agent config", cfg)
		} else if references(scx.AgentConfig(), uid) {
			affected = append(affected, scx)
		}
	}
	return affected
}

func (c *configWatcher) configsAffectedByWorkloads(ctx context.Context, nsData map[string]string, wls []k8sapi.Workload) []agentconfig.SidecarExt {
	var affected []agentconfig.SidecarExt
	for _, wl := range wls {
		if nsd, ok := nsData[wl.GetName()]; ok {
			scx, err := agentconfig.UnmarshalYAML([]byte(nsd))
			if err != nil {
				dlog.Errorf(ctx, "failed to decode ConfigMap entry %q into an agent config", nsd)
			} else {
				affected = append(affected, scx)
			}
		}
	}
	return affected
}

func (c *configWatcher) affectedConfigs(ctx context.Context, svc *core.Service, isDelete bool) []agentconfig.SidecarExt {
	ns := svc.Namespace

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

	c.RLock()
	defer c.RUnlock()
	nsData, ok := c.data[ns]

	if !ok || len(nsData) == 0 {
		return nil
	}

	if isDelete {
		return c.configsAffectedBySvcUID(ctx, nsData, svc.UID)
	}

	return c.configsAffectedByWorkloads(ctx, nsData, wls)
}

func (c *configWatcher) updateSvc(ctx context.Context, svc *core.Service, isDelete bool) {
	// Does the snapshot contain workloads that we didn't find using the service's Spec.Selector?
	// If so, include them, or if workload for the config entry isn't found, delete that entry
	img := managerutil.GetAgentImage(ctx)
	if img == "" {
		return
	}
	cfg, err := agentmap.GeneratorConfigFunc(img)
	if err != nil {
		dlog.Error(ctx, err)
		return
	}
	for _, scx := range c.affectedConfigs(ctx, svc, isDelete) {
		ac := scx.AgentConfig()
		wl, err := tracing.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
		if err != nil {
			if errors.IsNotFound(err) {
				dlog.Debugf(ctx, "Deleting config entry for %s %s.%s", ac.WorkloadKind, ac.WorkloadName, ac.Namespace)
				if err = c.remove(ctx, ac.AgentName, ac.Namespace); err != nil {
					dlog.Error(ctx, err)
				}
			} else {
				dlog.Error(ctx, err)
			}
			continue
		}
		dlog.Debugf(ctx, "Regenerating config entry for %s %s.%s", ac.WorkloadKind, ac.WorkloadName, ac.Namespace)
		acn, err := cfg.Generate(ctx, wl, ac)
		if err != nil {
			dlog.Error(ctx, err)
			continue
		}
		if err = c.store(ctx, acn, false); err != nil {
			dlog.Error(ctx, err)
		}
	}
}

func (c *configWatcher) svcEventHandler(ctx context.Context, evCh <-chan watch.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-evCh:
			if !ok {
				return // restart watcher
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
			scx, wl, err := e.workload(ctx)
			if err != nil {
				if !errors.IsNotFound(err) {
					dlog.Errorf(ctx, "unable to get workload for %s.%s %s: %v", k, ns, v, err)
				}
				continue
			}
			ac := scx.AgentConfig()
			if ac.Create || ac.Manual {
				// Deleted before it was generated or manually added, just ignore
				continue
			}
			triggerRollout(ctx, wl, nil)
		}
		if err := api.ConfigMaps(ns).Delete(ctx, agentconfig.ConfigMap, *now); err != nil {
			dlog.Errorf(ctx, "unable to delete ConfigMap %s-%s: %v", agentconfig.ConfigMap, ns, err)
		}
	}
}

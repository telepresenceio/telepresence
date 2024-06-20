package mutator

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/puzpuzpuz/xsync/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/durationpb"
	appsv1 "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	informerCore "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/retry"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/tracing"
)

type Map interface {
	Get(context.Context, string, string) (agentconfig.SidecarExt, error)
	Start(context.Context)
	Wait(context.Context) error
	OnAdd(context.Context, k8sapi.Workload, agentconfig.SidecarExt) error
	OnDelete(context.Context, string, string) error
	DeleteMapsAndRolloutAll(ctx context.Context)
	Blacklist(podName, namespace string)
	Whitelist(podName, namespace string)
	IsBlacklisted(podName, namespace string) bool

	store(ctx context.Context, acx agentconfig.SidecarExt) error
	remove(ctx context.Context, name, namespace string) error

	RegenerateAgentMaps(ctx context.Context, s string) error

	Delete(ctx context.Context, namespace string, name string) error
	Update(ctx context.Context, namespace string, updater func(cm *core.ConfigMap) (bool, error)) error
}

var NewWatcherFunc = NewWatcher //nolint:gochecknoglobals // extension point

type mapKey struct{}

func WithMap(ctx context.Context, m Map) context.Context {
	return context.WithValue(ctx, mapKey{}, m)
}

func GetMap(ctx context.Context) Map {
	if m, ok := ctx.Value(mapKey{}).(Map); ok {
		return m
	}
	return nil
}

func Load(ctx context.Context) (m Map) {
	cw := NewWatcherFunc(managerutil.GetEnv(ctx).ManagedNamespaces...)
	cw.Start(ctx)
	return cw
}

func (e *entry) workload(ctx context.Context) (agentconfig.SidecarExt, k8sapi.Workload, error) {
	scx, err := agentconfig.UnmarshalYAML([]byte(e.value))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode ConfigMap entry %q into an agent config", e.value)
	}
	ac := scx.AgentConfig()
	wl, err := agentmap.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
	if err != nil {
		return nil, nil, err
	}
	return scx, wl, nil
}

// isRolloutNeeded checks if the agent's entry in telepresence-agents matches the actual state of the
// pods. If it does, then there's no reason to trigger a rollout.
func (c *configWatcher) isRolloutNeeded(ctx context.Context, wl k8sapi.Workload, ac *agentconfig.Sidecar) bool {
	podLabels := wl.GetPodTemplate().GetObjectMeta().GetLabels()
	if len(podLabels) == 0 {
		// Have never seen this, but if it happens, then rollout only if an agent is desired
		dlog.Debugf(ctx, "Rollout of %s.%s is necessary. Pod template has no pod labels",
			wl.GetName(), wl.GetNamespace())
		return true
	}

	selector := labels.SelectorFromValidatedSet(podLabels)
	podsAPI := informer.GetFactory(ctx, wl.GetNamespace()).Core().V1().Pods().Lister().Pods(wl.GetNamespace())
	pods, err := podsAPI.List(selector)
	if err != nil {
		dlog.Debugf(ctx, "Rollout of %s.%s is necessary. Unable to retrieve current pods: %v",
			wl.GetName(), wl.GetNamespace(), err)
		return true
	}

	runningPods := 0
	okPods := 0
	var rolloutReasons []string
	for _, pod := range pods {
		if c.IsBlacklisted(pod.Name, pod.Namespace) {
			dlog.Debugf(ctx, "Skipping blacklisted pod %s.%s", pod.Name, pod.Namespace)
			continue
		}
		if !agentmap.IsPodRunning(pod) {
			continue
		}
		runningPods++
		if ror := isRolloutNeededForPod(ctx, ac, wl.GetName(), wl.GetNamespace(), pod); ror != "" {
			if !slices.Contains(rolloutReasons, ror) {
				rolloutReasons = append(rolloutReasons, ror)
			}
		} else {
			okPods++
		}
	}
	// Rollout if there are no running pods
	if runningPods == 0 {
		if ac != nil {
			dlog.Debugf(ctx, "Rollout of %s.%s is necessary. An agent is desired and there are no pods",
				wl.GetName(), wl.GetNamespace())
			return true
		}
		return false
	}
	if okPods == 0 {
		// Found no pods out there that matches the desired state
		for _, ror := range rolloutReasons {
			dlog.Debug(ctx, ror)
		}
		return true
	}
	dlog.Debugf(ctx, "Rollout of %s.%s is not necessary. At least one pod have the desired agent state",
		wl.GetName(), wl.GetNamespace())
	return false
}

func isRolloutNeededForPod(ctx context.Context, ac *agentconfig.Sidecar, name, namespace string, pod *core.Pod) string {
	podAc := agentmap.AgentContainer(pod)
	if ac == nil {
		if podAc == nil {
			return ""
		}
		return fmt.Sprintf("Rollout of %s.%s is necessary. No agent is desired but the pod %s has one", name, namespace, pod.GetName())
	}
	if podAc == nil {
		// Rollout because an agent is desired but the pod doesn't have one
		return fmt.Sprintf("Rollout of %s.%s is necessary. An agent is desired but the pod %s doesn't have one",
			name, namespace, pod.GetName())
	}
	desiredAc := agentconfig.AgentContainer(ctx, pod, ac)
	if !containerEqual(podAc, desiredAc) {
		return fmt.Sprintf("Rollout of %s.%s is necessary. The desired agent is not equal to the existing agent in pod %s",
			name, namespace, pod.GetName())
	}
	podIc := agentmap.InitContainer(pod)
	if podIc == nil {
		if needInitContainer(ac) {
			return fmt.Sprintf("Rollout of %s.%s is necessary. An init-container is desired but the pod %s doesn't have one",
				name, namespace, pod.GetName())
		}
	} else {
		if !needInitContainer(ac) {
			return fmt.Sprintf("Rollout of %s.%s is necessary. No init-container is desired but the pod %s has one",
				name, namespace, pod.GetName())
		}
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
			return fmt.Sprintf("Rollout of %s.%s is necessary. The desired pod should contain container %s",
				name, namespace, cn.Name)
		}
		if cn.Replace {
			// Ensure that the replaced container is disabled
			if !(found.Image == sleeperImage && slices.Equal(found.Args, sleeperArgs)) {
				return fmt.Sprintf("Rollout of %s.%s is necessary. The desired pod's container %s should be disabled",
					name, namespace, cn.Name)
			}
		} else {
			// Ensure that the replaced container is not disabled
			if found.Image == sleeperImage && slices.Equal(found.Args, sleeperArgs) {
				return fmt.Sprintf("Rollout of %s.%s is necessary. The desired pod's container %s should not be disabled",
					name, namespace, cn.Name)
			}
		}
	}
	return ""
}

func (c *configWatcher) triggerRollout(ctx context.Context, wl k8sapi.Workload, ac *agentconfig.Sidecar) {
	if !c.isRolloutNeeded(ctx, wl, ac) {
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

func triggerRolloutReplicaSet(ctx context.Context, wl k8sapi.Workload, rs *appsv1.ReplicaSet, span trace.Span) {
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
func (c *configWatcher) RegenerateAgentMaps(ctx context.Context, agentImage string) error {
	gc, err := agentmap.GeneratorConfigFunc(agentImage)
	if err != nil {
		return err
	}
	nss := managerutil.GetEnv(ctx).ManagedNamespaces
	if len(nss) == 0 {
		return c.regenerateAgentMaps(ctx, "", gc)
	}
	for _, ns := range nss {
		if err = c.regenerateAgentMaps(ctx, ns, gc); err != nil {
			return err
		}
	}
	return nil
}

// regenerateAgentMaps load the telepresence-agents config map, regenerates all entries in it,
// and then, if any of the entries changed, it updates the map.
func (c *configWatcher) regenerateAgentMaps(ctx context.Context, ns string, gc agentmap.GeneratorConfig) error {
	dlog.Debugf(ctx, "regenerate agent maps %s", whereWeWatch(ns))
	lister := tpAgentsInformer(ctx, ns).Lister()
	cml, err := lister.List(labels.Everything())
	if err != nil {
		return err
	}
	dbpCmp := cmp.Comparer(func(a, b *durationpb.Duration) bool {
		return a.AsDuration() == b.AsDuration()
	})

	n := len(cml)
	for i := 0; i < n; i++ {
		cm := cml[i]
		changed := false
		ns := cm.Namespace
		err = c.Update(ctx, ns, func(cm *core.ConfigMap) (bool, error) {
			dlog.Debugf(ctx, "regenerate: checking namespace %s", ns)
			data := cm.Data
			for n, d := range data {
				e := &entry{name: n, namespace: ns, value: d}
				acx, wl, err := e.workload(ctx)
				if err != nil {
					if !errors.IsNotFound(err) {
						return false, err
					}
					dlog.Debugf(ctx, "regenereate: no workload found %s", n)
					delete(data, n) // Workload no longer exists
					changed = true
					continue
				}
				ncx, err := gc.Generate(ctx, wl, acx)
				if err != nil {
					return false, err
				}
				if cmp.Equal(acx, ncx, dbpCmp) {
					dlog.Debugf(ctx, "regenereate: agent %s is not modified", n)
					continue
				}
				yml, err := ncx.Marshal()
				if err != nil {
					return false, err
				}
				dlog.Debugf(ctx, "%v != %v", acx, ncx)
				dlog.Debugf(ctx, "regenereate: agent %s was regenerated", n)
				data[n] = string(yml)
				changed = true
			}
			if changed {
				dlog.Debugf(ctx, "regenereate: updating regenerated agents")
			}
			return changed, nil
		})
	}
	return err
}

type configWatcher struct {
	cancel          context.CancelFunc
	nsLocks         *xsync.MapOf[string, *sync.RWMutex]
	blacklistedPods *xsync.MapOf[string, time.Time]

	cms []cache.SharedIndexInformer
	svs []cache.SharedIndexInformer

	self Map // For extension
}

// Blacklist will prevent the pod from being used when determining if a rollout is necessary, and
// from participating in ReviewIntercept calls. This is needed because there's a lag between the
// time when a pod is deleted and its agent announces its departure during which the pod must be
// considered inactive.
func (c *configWatcher) Blacklist(podName, namespace string) {
	c.blacklistedPods.Store(podName+"."+namespace, time.Now())
}

func (c *configWatcher) Whitelist(podName, namespace string) {
	c.blacklistedPods.Delete(podName + "." + namespace)
}

func (c *configWatcher) IsBlacklisted(podName, namespace string) bool {
	_, ok := c.blacklistedPods.Load(podName + "." + namespace)
	return ok
}

func (c *configWatcher) Delete(ctx context.Context, namespace string, name string) error {
	return c.remove(ctx, name, namespace)
}

func (c *configWatcher) Update(ctx context.Context, namespace string, updater func(cm *core.ConfigMap) (bool, error)) error {
	api := k8sapi.GetK8sInterface(ctx).CoreV1().ConfigMaps(namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = derror.PanicToError(r)
				dlog.Errorf(ctx, "%+v", err)
			}
		}()
		lock := c.getNamespaceLock(namespace)
		lock.Lock()
		defer lock.Unlock()
		cm, err := tpAgentsConfigMap(ctx, namespace)
		if err != nil {
			return err
		}
		cm = cm.DeepCopy() // Protect the cached cm from updates
		create := cm == nil
		if create {
			cm = &core.ConfigMap{
				TypeMeta: meta.TypeMeta{
					Kind:       "ConfigMap",
					APIVersion: "v1",
				},
				ObjectMeta: meta.ObjectMeta{
					Name:      agentconfig.ConfigMap,
					Namespace: namespace,
				},
			}
		}

		changed, err := updater(cm)
		if err == nil && changed {
			if create {
				_, err = api.Create(ctx, cm, meta.CreateOptions{})
				if err != nil && errors.IsAlreadyExists(err) {
					// Treat AlreadyExists as a Conflict so that this attempt is retried.
					err = errors.NewConflict(schema.GroupResource{
						Group:    "v1",
						Resource: "ConfigMap",
					}, cm.Name, err)
				}
			} else {
				_, err = api.Update(ctx, cm, meta.UpdateOptions{})
			}
		}
		return err
	})
}

func NewWatcher(namespaces ...string) Map {
	w := &configWatcher{
		nsLocks:         xsync.NewMapOf[string, *sync.RWMutex](),
		blacklistedPods: xsync.NewMapOf[string, time.Time](),
	}
	if len(namespaces) > 0 {
		for _, ns := range namespaces {
			w.getNamespaceLock(ns)
		}
	}
	w.self = w
	return w
}

type entry struct {
	name      string
	namespace string
	value     string
	oldValue  string
}

func (c *configWatcher) SetSelf(self Map) {
	c.self = self
}

func (c *configWatcher) Wait(ctx context.Context) error {
	ctx, c.cancel = context.WithCancel(ctx)
	for _, si := range c.svs {
		if err := c.watchServices(ctx, si); err != nil {
			return err
		}
	}
	for _, ci := range c.cms {
		if err := c.watchConfigMap(ctx, ci); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

func (c *configWatcher) OnAdd(ctx context.Context, wl k8sapi.Workload, acx agentconfig.SidecarExt) error {
	c.triggerRollout(ctx, wl, acx.AgentConfig())
	return nil
}

func (c *configWatcher) OnDelete(context.Context, string, string) error {
	return nil
}

func (c *configWatcher) handleAddOrUpdateEntry(ctx context.Context, e entry) {
	dlog.Debugf(ctx, "add %s.%s", e.name, e.namespace)
	scx, wl, err := e.workload(ctx)
	if err != nil {
		if !errors.IsNotFound(err) {
			dlog.Error(ctx, err)
		}
		return
	}
	ac := scx.AgentConfig()
	if ac.Manual {
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
		if scx, err = gc.Generate(ctx, wl, ac); err != nil {
			dlog.Error(ctx, err)
		} else if err = c.store(ctx, scx); err != nil { // Calling store() will generate a new event, so we skip rollout here
			dlog.Error(ctx, err)
		}
		return
	}
	if err = c.self.OnAdd(ctx, wl, scx); err != nil {
		dlog.Error(ctx, err)
	}
}

func (c *configWatcher) handleDeleteEntry(ctx context.Context, e entry) {
	dlog.Debugf(ctx, "del %s.%s", e.name, e.namespace)
	scx, wl, err := e.workload(ctx)
	if err != nil {
		if !errors.IsNotFound(err) {
			dlog.Error(ctx, err)
			return
		}
	} else {
		ac := scx.AgentConfig()
		if ac.Create || ac.Manual {
			// Deleted before it was generated or manually added, just ignore
			return
		}
	}
	if err = c.self.OnDelete(ctx, e.name, e.namespace); err != nil {
		dlog.Error(ctx, err)
	}
	if wl != nil {
		c.triggerRollout(ctx, wl, nil)
	}
}

func (c *configWatcher) getNamespaceLock(ns string) *sync.RWMutex {
	lock, _ := c.nsLocks.LoadOrCompute(ns, func() *sync.RWMutex {
		return &sync.RWMutex{}
	})
	return lock
}

func (c *configWatcher) Get(ctx context.Context, key, ns string) (agentconfig.SidecarExt, error) {
	lock := c.getNamespaceLock(ns)
	lock.RLock()
	defer lock.RUnlock()

	data, err := data(ctx, ns)
	if err != nil {
		return nil, err
	}
	v, ok := data[key]
	if !ok {
		return nil, nil
	}
	return agentconfig.UnmarshalYAML([]byte(v))
}

// remove will delete an agent config from the agents ConfigMap for the given namespace. It will
// also update the current snapshot.
// An attempt to delete a manually added config is a no-op.
func (c *configWatcher) remove(ctx context.Context, name, namespace string) error {
	return c.Update(ctx, namespace, func(cm *core.ConfigMap) (bool, error) {
		yml, ok := cm.Data[name]
		if !ok {
			return false, nil
		}
		scx, err := agentconfig.UnmarshalYAML([]byte(yml))
		if err != nil {
			return false, err
		}
		if scx.AgentConfig().Manual {
			return false, nil
		}
		delete(cm.Data, name)
		dlog.Debugf(ctx, "Deleting %s from ConfigMap %s.%s", name, agentconfig.ConfigMap, namespace)
		return true, nil
	})
}

// store an agent config in the agents ConfigMap for the given namespace.
func (c *configWatcher) store(ctx context.Context, acx agentconfig.SidecarExt) error {
	js, err := acx.Marshal()
	yml := string(js)
	if err != nil {
		return err
	}
	ac := acx.AgentConfig()
	ns := ac.Namespace
	return c.Update(ctx, ns, func(cm *core.ConfigMap) (bool, error) {
		if cm.Data == nil {
			cm.Data = make(map[string]string)
		} else {
			if oldYml, ok := cm.Data[ac.AgentName]; ok {
				if oldYml == yml {
					return false, nil
				}
				scx, err := agentconfig.UnmarshalYAML([]byte(oldYml))
				if err == nil && scx.AgentConfig().Manual {
					dlog.Warnf(ctx, "avoided an attempt to overwrite manually added Config entry for %s.%s", ac.AgentName, ns)
					return false, nil
				}
			}
		}
		cm.Data[ac.AgentName] = yml
		dlog.Debugf(ctx, "updating agent %s in %s.%s", ac.AgentName, agentconfig.ConfigMap, ns)
		return true, nil
	})
}

func tpAgentsInformer(ctx context.Context, ns string) informerCore.ConfigMapInformer {
	f := informer.GetFactory(ctx, ns)
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

func data(ctx context.Context, ns string) (map[string]string, error) {
	cm, err := tpAgentsConfigMap(ctx, ns)
	if err != nil || cm == nil {
		return nil, err
	}
	return cm.Data, nil
}

func whereWeWatch(ns string) string {
	if ns == "" {
		return "cluster wide"
	}
	return "in namespace " + ns
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

func (c *configWatcher) startServices(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetFactory(ctx, ns)
	ix := f.Core().V1().Services().Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		// Strip of the parts of the service that we don't care about
		if svc, ok := o.(*core.Service); ok {
			svc.ManagedFields = nil
			svc.Status = core.ServiceStatus{}
			svc.Finalizers = nil
			svc.OwnerReferences = nil
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		dlog.Errorf(ctx, "watcher for Services %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func (c *configWatcher) watchServices(ctx context.Context, ix cache.SharedIndexInformer) error {
	_, err := ix.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj any) {
				if svc, ok := obj.(*core.Service); ok {
					c.updateSvc(ctx, svc, false)
				}
			},
			DeleteFunc: func(obj any) {
				if svc, ok := obj.(*core.Service); ok {
					c.updateSvc(ctx, svc, true)
				} else if dfsu, ok := obj.(*cache.DeletedFinalStateUnknown); ok {
					if svc, ok := dfsu.Obj.(*core.Service); ok {
						c.updateSvc(ctx, svc, true)
					}
				}
			},
			UpdateFunc: func(oldObj, newObj any) {
				if newSvc, ok := newObj.(*core.Service); ok {
					c.updateSvc(ctx, newSvc, true)
				}
			},
		})
	return err
}

func (c *configWatcher) startPods(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetFactory(ctx, ns)
	ix := f.Core().V1().Pods().Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		if pod, ok := o.(*core.Pod); ok {
			pod.ManagedFields = nil
			pod.OwnerReferences = nil
			pod.Finalizers = nil

			ps := &pod.Status
			// We're just interested in the podIP/podIPs
			ps.Conditions = nil

			// Strip everything but the State from the container statuses. We need
			// the state to determine if a pod is running.
			cns := pod.Status.ContainerStatuses
			for i := range cns {
				cns[i] = core.ContainerStatus{
					State: cns[i].State,
				}
			}
			ps.EphemeralContainerStatuses = nil
			ps.HostIPs = nil
			ps.HostIP = ""
			ps.InitContainerStatuses = nil
			ps.Message = ""
			ps.ResourceClaimStatuses = nil
			ps.NominatedNodeName = ""
			ps.Reason = ""
			ps.Resize = ""
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		dlog.Errorf(ctx, "Watcher for pods %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func (c *configWatcher) startDeployments(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetFactory(ctx, ns)
	ix := f.Apps().V1().Deployments().Informer()
	_ = ix.SetTransform(func(o any) (any, error) {
		// Strip of the parts of the deployment that we don't care about. Saves memory
		if dep, ok := o.(*appsv1.Deployment); ok {
			dep.ManagedFields = nil
			dep.Status = appsv1.DeploymentStatus{}
			dep.Finalizers = nil
			dep.OwnerReferences = nil
		}
		return o, nil
	})
	_ = ix.SetWatchErrorHandler(func(_ *cache.Reflector, err error) {
		dlog.Errorf(ctx, "watcher for Deployments %s: %v", whereWeWatch(ns), err)
	})
	return ix
}

func (c *configWatcher) gcBlacklisted(now time.Time) {
	const maxAge = time.Minute
	maxCreated := now.Add(-maxAge)
	c.blacklistedPods.Range(func(key string, created time.Time) bool {
		if created.Before(maxCreated) {
			c.blacklistedPods.Delete(key)
		}
		return true
	})
}

func (c *configWatcher) Start(ctx context.Context) {
	env := managerutil.GetEnv(ctx)
	nss := env.ManagedNamespaces
	if len(nss) == 0 {
		nss = []string{""}
	}

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				return
			case now := <-ticker.C:
				c.gcBlacklisted(now)
			}
		}
	}()

	c.svs = make([]cache.SharedIndexInformer, len(nss))
	c.cms = make([]cache.SharedIndexInformer, len(nss))
	for i, ns := range nss {
		c.cms[i] = c.startConfigMap(ctx, ns)
		c.svs[i] = c.startServices(ctx, ns)
		c.startDeployments(ctx, ns)
		c.startPods(ctx, ns)
		f := informer.GetFactory(ctx, ns)
		f.Start(ctx.Done())
		f.WaitForCacheSync(ctx.Done())
	}
}

type affectedConfig struct {
	err error
	wl  k8sapi.Workload // If a workload is retrieved, it will be cached here.
	scx agentconfig.SidecarExt
}

func (c *configWatcher) configsAffectedBySvc(ctx context.Context, nsData map[string]string, svc *core.Service, trustUID bool) []affectedConfig {
	references := func(ac *agentconfig.Sidecar) (k8sapi.Workload, error, bool) {
		for _, cn := range ac.Containers {
			for _, ic := range cn.Intercepts {
				if ic.ServiceUID == svc.UID {
					return nil, nil, true
				}
			}
		}
		if trustUID {
			// A deleted service will only affect configs that matches its UID
			return nil, nil, false
		}

		// The config will be affected if a service is added or modified so that it now selects the pod for the workload.
		wl, err := agentmap.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
		if err != nil {
			return nil, err, false
		}
		return wl, nil, labels.SelectorFromSet(svc.Spec.Selector).Matches(labels.Set(wl.GetPodTemplate().Labels))
	}

	var affected []affectedConfig
	for _, cfg := range nsData {
		scx, err := agentconfig.UnmarshalYAML([]byte(cfg))
		if err != nil {
			dlog.Errorf(ctx, "failed to decode ConfigMap entry %q into an agent config", cfg)
		} else if wl, err, ok := references(scx.AgentConfig()); ok {
			affected = append(affected, affectedConfig{scx: scx, wl: wl, err: err})
		}
	}
	return affected
}

func (c *configWatcher) affectedConfigs(ctx context.Context, svc *core.Service, trustUID bool) []affectedConfig {
	ns := svc.Namespace
	nsData, err := data(ctx, ns)
	if err != nil {
		return nil
	}
	if len(nsData) == 0 {
		return nil
	}
	return c.configsAffectedBySvc(ctx, nsData, svc, trustUID)
}

func (c *configWatcher) updateSvc(ctx context.Context, svc *core.Service, trustUID bool) {
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
	for _, ax := range c.affectedConfigs(ctx, svc, trustUID) {
		ac := ax.scx.AgentConfig()
		wl := ax.wl
		if wl == nil {
			err = ax.err
			if err == nil {
				wl, err = agentmap.GetWorkload(ctx, ac.WorkloadName, ac.Namespace, ac.WorkloadKind)
			}
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
		}
		dlog.Debugf(ctx, "Regenerating config entry for %s %s.%s", ac.WorkloadKind, ac.WorkloadName, ac.Namespace)
		acn, err := cfg.Generate(ctx, wl, ac)
		if err != nil {
			if strings.Contains(err.Error(), "unable to find") {
				if err = c.remove(ctx, ac.AgentName, ac.Namespace); err != nil {
					dlog.Error(ctx, err)
				}
			} else {
				dlog.Error(ctx, err)
			}
			continue
		}
		if err = c.store(ctx, acn); err != nil {
			dlog.Error(ctx, err)
		}
	}
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
		if oldYml, ok := oldCm.Data[n]; ok && oldYml != newYml {
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

func (c *configWatcher) DeleteMapsAndRolloutAll(ctx context.Context) {
	c.cancel() // No more updates from watcher
	now := meta.NewDeleteOptions(0)
	api := k8sapi.GetK8sInterface(ctx).CoreV1()
	c.nsLocks.Range(func(ns string, lock *sync.RWMutex) bool {
		lock.Lock()
		defer lock.Unlock()
		wlm, err := data(ctx, ns)
		if err != nil {
			dlog.Errorf(ctx, "unable to get configmap %s.%s: %v", agentconfig.ConfigMap, ns, err)
			return true
		}
		for k, v := range wlm {
			e := &entry{name: k, namespace: ns, value: v}
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
			c.triggerRollout(ctx, wl, nil)
		}
		if err := api.ConfigMaps(ns).Delete(ctx, agentconfig.ConfigMap, *now); err != nil {
			dlog.Errorf(ctx, "unable to delete ConfigMap %s-%s: %v", agentconfig.ConfigMap, ns, err)
		}
		return true
	})
}

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
	StartWatchers(ctx context.Context) error
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

	Delete(ctx context.Context, name, namespace string) error
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
	podMeta := wl.GetPodTemplate().GetObjectMeta()
	if wl.GetDeletionTimestamp() != nil {
		return false
	}
	if ia, ok := podMeta.GetAnnotations()[agentconfig.InjectAnnotation]; ok {
		// Annotation controls injection, so no explicit rollout is needed unless the deployment was added after the traffic-manager.
		// If the annotation changes, there will be an implicit rollout anyway.
		if podMeta.GetCreationTimestamp().After(c.startedAt) {
			dlog.Debugf(ctx, "Rollout of %s.%s is not necessary. Pod template has inject annotation %s",
				wl.GetName(), wl.GetNamespace(), ia)
			return false
		}
	}
	podLabels := podMeta.GetLabels()
	if len(podLabels) == 0 {
		// Have never seen this, but if it happens, then rollout only if an agent is desired
		dlog.Debugf(ctx, "Rollout of %s.%s is necessary. Pod template has no pod labels",
			wl.GetName(), wl.GetNamespace())
		return true
	}

	selector := labels.SelectorFromValidatedSet(podLabels)
	podsAPI := informer.GetK8sFactory(ctx, wl.GetNamespace()).Core().V1().Pods().Lister().Pods(wl.GetNamespace())
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
			dlog.Debugf(ctx, "Rollout check for %s.%s is found that no agent is desired and no agent config is present for pod %s", name, namespace, pod.GetName())
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

const AnnRestartedAt = DomainPrefix + "restartedAt"

func (c *configWatcher) triggerRollout(ctx context.Context, wl k8sapi.Workload, ac *agentconfig.Sidecar) {
	lck := c.getRolloutLock(wl)
	if !lck.TryLock() {
		// A rollout is already in progress, doing it again once it is complete wouldn't do any good.
		return
	}
	defer lck.Unlock()

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

	restartAnnotation := generateRestartAnnotationPatch(wl.GetPodTemplate())
	span.AddEvent("tel2.do-rollout")
	if err := wl.Patch(ctx, types.JSONPatchType, []byte(restartAnnotation)); err != nil {
		err = fmt.Errorf("unable to patch %s %s.%s: %v", wl.GetKind(), wl.GetName(), wl.GetNamespace(), err)
		dlog.Error(ctx, err)
		span.SetStatus(codes.Error, err.Error())
		return
	}
	dlog.Infof(ctx, "Successfully rolled out %s.%s", wl.GetName(), wl.GetNamespace())
}

// generateRestartAnnotationPatch generates a JSON patch that adds or updates the annotation
// We need to use this particular patch type because argo-rollouts does not support strategic merge patches.
func generateRestartAnnotationPatch(podTemplate *core.PodTemplateSpec) string {
	basePointer := "/spec/template/metadata/annotations"
	pointer := fmt.Sprintf(
		basePointer+"/%s",
		strings.ReplaceAll(AnnRestartedAt, "/", "~1"),
	)

	if _, ok := podTemplate.Annotations[AnnRestartedAt]; ok {
		return fmt.Sprintf(
			`[{"op": "replace", "path": "%s", "value": "%s"}]`, pointer, time.Now().Format(time.RFC3339),
		)
	}

	if len(podTemplate.Annotations) == 0 {
		return fmt.Sprintf(
			`[{"op": "add", "path": "%s", "value": {}}, {"op": "add", "path": "%s", "value": "%s"}]`, basePointer, pointer, time.Now().Format(time.RFC3339),
		)
	}

	return fmt.Sprintf(
		`[{"op": "add", "path": "%s", "value": "%s"}]`, pointer, time.Now().Format(time.RFC3339),
	)
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

type workloadKey struct {
	name      string
	namespace string
	kind      string
}

type configWatcher struct {
	cancel          context.CancelFunc
	rolloutLocks    *xsync.MapOf[workloadKey, *sync.Mutex]
	nsLocks         *xsync.MapOf[string, *sync.RWMutex]
	blacklistedPods *xsync.MapOf[string, time.Time]
	startedAt       time.Time

	cms []cache.SharedIndexInformer
	svs []cache.SharedIndexInformer
	dps []cache.SharedIndexInformer
	rss []cache.SharedIndexInformer
	sss []cache.SharedIndexInformer
	rls []cache.SharedIndexInformer

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

func (c *configWatcher) Delete(ctx context.Context, name, namespace string) error {
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
		rolloutLocks:    xsync.NewMapOf[workloadKey, *sync.Mutex](),
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

func (c *configWatcher) StartWatchers(ctx context.Context) error {
	c.startedAt = time.Now()
	ctx, c.cancel = context.WithCancel(ctx)
	for _, si := range c.svs {
		if err := c.watchServices(ctx, si); err != nil {
			return err
		}
	}
	for _, si := range c.dps {
		if err := c.watchWorkloads(ctx, si); err != nil {
			return err
		}
	}
	for _, si := range c.rss {
		if err := c.watchWorkloads(ctx, si); err != nil {
			return err
		}
	}
	for _, si := range c.sss {
		if err := c.watchWorkloads(ctx, si); err != nil {
			return err
		}
	}
	if c.rls != nil {
		for _, si := range c.rls {
			if err := c.watchWorkloads(ctx, si); err != nil {
				return err
			}
		}
	}
	for _, ci := range c.cms {
		if err := c.watchConfigMap(ctx, ci); err != nil {
			return err
		}
	}
	return nil
}

func (c *configWatcher) Wait(ctx context.Context) error {
	if err := c.StartWatchers(ctx); err != nil {
		return err
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
	switch e.oldValue {
	case e.value:
		return
	case "":
		dlog.Debugf(ctx, "add %s.%s", e.name, e.namespace)
	default:
		dlog.Debugf(ctx, "update %s.%s", e.name, e.namespace)
	}
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

func (c *configWatcher) getRolloutLock(wl k8sapi.Workload) *sync.Mutex {
	lock, _ := c.rolloutLocks.LoadOrCompute(workloadKey{
		name:      wl.GetName(),
		namespace: wl.GetNamespace(),
		kind:      wl.GetKind(),
	}, func() *sync.Mutex {
		return &sync.Mutex{}
	})
	return lock
}

// Get returns the Sidecar configuration that for the given key and namespace.
// If no configuration is found, this function returns nil, nil.
// An error is only returned when the configmap holding the configuration could not be loaded for
// other reasons than it did not exist.
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
				dlog.Debugf(ctx, "Modifying configmap entry for sidecar %s.%s", ac.AgentName, ac.Namespace)
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

func (c *configWatcher) startPods(ctx context.Context, ns string) cache.SharedIndexInformer {
	f := informer.GetK8sFactory(ctx, ns)
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
	c.dps = make([]cache.SharedIndexInformer, len(nss))
	c.rss = make([]cache.SharedIndexInformer, len(nss))
	c.sss = make([]cache.SharedIndexInformer, len(nss))
	for i, ns := range nss {
		c.cms[i] = c.startConfigMap(ctx, ns)
		c.svs[i] = c.startServices(ctx, ns)
		c.dps[i] = c.startDeployments(ctx, ns)
		c.rss[i] = c.startReplicaSets(ctx, ns)
		c.sss[i] = c.startStatefulSets(ctx, ns)
		c.startPods(ctx, ns)
		kf := informer.GetK8sFactory(ctx, ns)
		kf.Start(ctx.Done())
		kf.WaitForCacheSync(ctx.Done())
	}
	if managerutil.ArgoRolloutsEnabled(ctx) {
		c.rls = make([]cache.SharedIndexInformer, len(nss))
		for i, ns := range nss {
			c.rls[i] = c.startRollouts(ctx, ns)
			rf := informer.GetArgoRolloutsFactory(ctx, ns)
			rf.Start(ctx.Done())
			rf.WaitForCacheSync(ctx.Done())
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

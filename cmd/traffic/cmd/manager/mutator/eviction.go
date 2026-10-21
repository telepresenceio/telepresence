package mutator

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	argoRollouts "github.com/datawire/argo-rollouts-go-client/pkg/apis/rollouts/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/puzpuzpuz/xsync/v4"
	"google.golang.org/protobuf/types/known/durationpb"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	v1 "k8s.io/api/policy/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type wlPods struct {
	wl   k8sapi.Workload
	pods []*core.Pod
}

type wlPodMap map[WorkloadKey]*wlPods

func (em wlPodMap) add(wl k8sapi.Workload, pod *core.Pod) {
	k := workloadKeyOf(wl)
	if v, ok := em[k]; ok {
		v.pods = append(v.pods, pod)
	} else {
		em[k] = &wlPods{wl: wl, pods: []*core.Pod{pod}}
	}
}

func workloadKeyOf(wl k8sapi.Workload) WorkloadKey {
	return WorkloadKey{Kind: wl.GetKind(), Name: wl.GetName(), Namespace: wl.GetNamespace()}
}

// EvictPodsWithAgentConfigMismatch requests that every pod of wl whose agent config differs from sc
// is replaced. The replacement is paced by the eviction queue.
func (c *configWatcher) EvictPodsWithAgentConfigMismatch(ctx context.Context, wl k8sapi.Workload, sc *agentconfig.Sidecar) error {
	c.requestEviction(ctx, wl, sc)
	return nil
}

// EvictPodsWithAgentConfig requests that every pod of wl that carries an agent is replaced by one
// without it.
func (c *configWatcher) EvictPodsWithAgentConfig(ctx context.Context, wl k8sapi.Workload) error {
	c.requestEviction(ctx, wl, nil)
	return nil
}

// EvictAllPodsWithAgentConfig removes the agent from every pod in the namespace at once. This is the
// teardown path, bounded by the Helm hook's request timeout, so it is not paced.
func (c *configWatcher) EvictAllPodsWithAgentConfig(ctx context.Context, namespace string) error {
	c.deleteNamespaceAgentConfigs(namespace)
	evictMap, err := podList(ctx, namespace)
	if err != nil {
		return err
	}
	var errs error
	for _, wp := range evictMap {
		if err = c.evictAll(ctx, wp.wl, wp.pods); err != nil {
			errs = errors.Join(errs, fmt.Errorf("unable to evict agents from %s: %w", wp.wl, err))
		}
	}
	return errs
}

func (c *configWatcher) evictAll(ctx context.Context, wl k8sapi.Workload, pods []*core.Pod) error {
	state := c.lockEvictionState(workloadKeyOf(wl))
	defer state.Unlock()
	c.finishRequest(state)
	_, refused, err := c.evictBatch(ctx, podsWithAgentConfigMismatch(ctx, pods, nil), len(pods))
	if err == nil && refused != nil {
		err = triggerRollout(ctx, wl, refused)
	}
	return err
}

// podsWithAgentConfigMismatch returns the pods that must be replaced for all of them to carry the
// wanted agent config. A nil wanted means no agent.
func podsWithAgentConfigMismatch(ctx context.Context, pods []*core.Pod, wanted *agentconfig.Sidecar) []*core.Pod {
	wantedTight, err := tightAgentConfig(wanted)
	if err != nil {
		clog.Errorf(ctx, "unable to marshal agent config: %v", err)
		return nil
	}
	return slices.DeleteFunc(slices.Clone(pods), func(pod *core.Pod) bool {
		switch {
		case pod.DeletionTimestamp != nil:
			clog.Tracef(ctx, "Keeping pod %s because it is already terminating", pod.Name)
		case podIsManuallyInjected(ctx, pod):
			clog.Tracef(ctx, "Keeping pod %s because it is managed manually", pod.Name)
		case podHasAgentConfig(ctx, pod, wantedTight):
			clog.Tracef(ctx, "Keeping pod %s because its config is still valid", pod.Name)
		default:
			return false
		}
		return true
	})
}

// tightAgentConfig returns the config as a pod annotation carries it, with the creation-only fields
// that MarshalTight strips absent.
func tightAgentConfig(sc *agentconfig.Sidecar) (*agentconfig.Sidecar, error) {
	if sc == nil {
		return nil, nil
	}
	cfgJSON, err := agentconfig.MarshalTight(sc)
	if err != nil {
		return nil, err
	}
	return agentconfig.UnmarshalJSON(cfgJSON)
}

// podHasAgentConfig reports whether the pod's annotation carries wantedTight, compared as values so
// that formatting differences between manager versions do not count.
func podHasAgentConfig(ctx context.Context, pod *core.Pod, wantedTight *agentconfig.Sidecar) bool {
	cfgJSON := pod.Annotations[annotation.Config]
	if cfgJSON == "" {
		return wantedTight == nil
	}
	if wantedTight == nil {
		return false
	}
	sc, err := agentconfig.UnmarshalJSON(cfgJSON)
	if err != nil {
		clog.Errorf(ctx, "unable to unmarshal agent config from annotation in pod %s.%s: %v", pod.Name, pod.Namespace, err)
		return false
	}
	return cmp.Equal(sc, wantedTight, cmpopts.EquateEmpty(), cmp.Comparer(func(x, y *durationpb.Duration) bool {
		return x.AsDuration() == y.AsDuration()
	}))
}

func podIsManuallyInjected(ctx context.Context, pod *core.Pod) bool {
	v := annotation.GetAnnotation(ctx, pod.Annotations, annotation.ManuallyInjected, annotation.LegacyManuallyInjected)
	return v == "true"
}

type podEvictionResult uint8

const (
	// podEvictionNone means nothing happened.
	podEvictionNone podEvictionResult = iota
	// podEvictionGone means the pod was already absent or replaced.
	podEvictionGone
	// podEvictionAccepted means the eviction was accepted.
	podEvictionAccepted
	// podEvictionRefused means a PodDisruptionBudget refused the eviction.
	podEvictionRefused
)

// evictBatch evicts up to budget of the pods. The batch ends at the first pod whose eviction the
// disruption budget refuses, and that pod is returned.
func (c *configWatcher) evictBatch(ctx context.Context, pods []*core.Pod, budget int) (evicted int, refused *core.Pod, err error) {
	for _, pod := range pods {
		if evicted >= budget {
			break
		}
		podID := pod.UID
		if c.isEvicted(podID) {
			clog.Debugf(ctx, "Skipping pod %s because it is already deleted", pod.Name)
			continue
		}
		result := podEvictionNone
		c.inactivePods.Compute(podID, func(v inactivation, loaded bool) (inactivation, xsync.ComputeOp) {
			if loaded && v.deleted {
				return v, xsync.CancelOp
			}
			result, err = tryEvictPod(ctx, pod)
			if err != nil || result == podEvictionRefused {
				return v, xsync.CancelOp
			}
			return inactivation{Time: time.Now(), deleted: true}, xsync.UpdateOp
		})
		if err != nil {
			return evicted, nil, err
		}
		switch result {
		case podEvictionAccepted:
			evicted++
		case podEvictionRefused:
			return evicted, pod, nil
		case podEvictionNone, podEvictionGone:
		}
	}
	return evicted, nil, nil
}

type disruptionBudgetError struct {
	error
	podName string
}

func (e disruptionBudgetError) Unwrap() error { return e.error }

func (e disruptionBudgetError) Error() string {
	return fmt.Sprintf("cannot evict pod %s as it would violate the pod's disruption budget", e.podName)
}

func tryEvictPod(ctx context.Context, pod *core.Pod) (podEvictionResult, error) {
	evicted, err := evictPod(ctx, pod)
	switch {
	case err == nil && evicted:
		return podEvictionAccepted, nil
	case err == nil:
		return podEvictionGone, nil
	case errors.As(err, &disruptionBudgetError{}):
		clog.Debug(ctx, err.Error())
		return podEvictionRefused, nil
	default:
		return podEvictionNone, fmt.Errorf("failed to evict pod %s: %w", pod.Name, err)
	}
}

// triggerRollout makes the workload's controller replace its pods.
func triggerRollout(ctx context.Context, wl k8sapi.Workload, pod *core.Pod) error {
	switch wl.GetKind() {
	case k8sapi.StatefulSetKind, k8sapi.ReplicaSetKind:
		return triggerScalingEviction(ctx, wl, pod)
	default:
		clog.Debugf(ctx, "Patching %s to trigger pod recreation", wl)
		restartAnnotation := generateRestartAnnotationPatch(wl.GetPodTemplate().Annotations)
		if err := wl.Patch(ctx, types.JSONPatchType, []byte(restartAnnotation)); err != nil {
			return fmt.Errorf("unable to patch %s: %w", wl, err)
		}
		clog.Debugf(ctx, "Successfully patched %s", wl)
		return nil
	}
}

// allowedUnavailable is the number of pods the workload's own rolling update would take down at once.
func allowedUnavailable(wl k8sapi.Workload) int {
	desired := int(k8sapi.DesiredReplicas(wl))
	defaultMaxUnavailable := intstr.FromString("25%")
	var maxUnavailable *intstr.IntOrString
	switch wl.GetKind() {
	case k8sapi.DeploymentKind:
		if d, ok := k8sapi.DeploymentImpl(wl); ok {
			if d.Spec.Strategy.Type == apps.RecreateDeploymentStrategyType {
				return desired
			}
			maxUnavailable = &defaultMaxUnavailable
			if ru := d.Spec.Strategy.RollingUpdate; ru != nil && ru.MaxUnavailable != nil {
				maxUnavailable = ru.MaxUnavailable
			}
		}
	case k8sapi.RolloutKind:
		if r, ok := k8sapi.RolloutImpl(wl); ok && r.Spec.Strategy.Canary != nil {
			maxUnavailable = &defaultMaxUnavailable
			if r.Spec.Strategy.Canary.MaxUnavailable != nil {
				maxUnavailable = r.Spec.Strategy.Canary.MaxUnavailable
			}
		}
	case k8sapi.StatefulSetKind:
		if ss, ok := k8sapi.StatefulSetImpl(wl); ok {
			if ru := ss.Spec.UpdateStrategy.RollingUpdate; ru != nil {
				maxUnavailable = ru.MaxUnavailable
			}
		}
	}
	if maxUnavailable == nil {
		return 1
	}
	n, err := intstr.GetScaledValueFromIntOrPercent(maxUnavailable, desired, false)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

func podIsReady(pod *core.Pod) bool {
	return pod.DeletionTimestamp == nil && pod.Status.Phase == core.PodRunning &&
		slices.ContainsFunc(pod.Status.Conditions, func(condition core.PodCondition) bool {
			return condition.Type == core.PodReady && condition.Status == core.ConditionTrue
		})
}

// unavailablePods counts the desired replicas that have no ready pod, including those evicted so
// recently that the cache has no replacement for them yet.
func unavailablePods(wl k8sapi.Workload, pods []*core.Pod) int {
	ready := 0
	for _, pod := range pods {
		if podIsReady(pod) {
			ready++
		}
	}
	return max(0, int(k8sapi.DesiredReplicas(wl))-ready)
}

// workloadRolloutInProgress reports whether the workload's controller is replacing pods. A workload
// that is merely short of ready replicas is not in progress; that is the budget's concern.
func workloadRolloutInProgress(wl k8sapi.Workload) bool {
	desiredReplicas := k8sapi.DesiredReplicas(wl)
	switch wl.GetKind() {
	case k8sapi.DeploymentKind:
		deployment, ok := k8sapi.DeploymentImpl(wl)
		if !ok {
			break
		}
		if deployment.Status.ObservedGeneration != deployment.Generation ||
			deployment.Status.Replicas != desiredReplicas ||
			deployment.Status.UpdatedReplicas != desiredReplicas {
			return true
		}
		for _, condition := range deployment.Status.Conditions {
			if condition.Type == apps.DeploymentProgressing && condition.Status == core.ConditionTrue {
				return condition.Reason != "NewReplicaSetAvailable"
			}
		}
		return false
	case k8sapi.RolloutKind:
		rollout, ok := k8sapi.RolloutImpl(wl)
		if !ok {
			break
		}
		return rollout.Status.ObservedGeneration != strconv.FormatInt(rollout.Generation, 10) ||
			rollout.Status.Replicas != desiredReplicas ||
			rollout.Status.UpdatedReplicas != desiredReplicas ||
			rollout.Status.ReadyReplicas != desiredReplicas ||
			rollout.Status.AvailableReplicas != desiredReplicas ||
			rollout.Status.Phase != argoRollouts.RolloutPhaseHealthy
	case k8sapi.ReplicaSetKind:
		replicaSet, ok := k8sapi.ReplicaSetImpl(wl)
		if !ok {
			break
		}
		return replicaSet.Status.ObservedGeneration != replicaSet.Generation ||
			replicaSet.Status.Replicas != desiredReplicas
	case k8sapi.StatefulSetKind:
		statefulSet, ok := k8sapi.StatefulSetImpl(wl)
		if !ok {
			break
		}
		if statefulSet.Status.ObservedGeneration != statefulSet.Generation ||
			statefulSet.Status.Replicas != desiredReplicas {
			return true
		}
		if statefulSet.Spec.UpdateStrategy.Type == apps.OnDeleteStatefulSetStrategyType {
			return false
		}
		expectedUpdatedReplicas := desiredReplicas
		if rollingUpdate := statefulSet.Spec.UpdateStrategy.RollingUpdate; rollingUpdate != nil && rollingUpdate.Partition != nil {
			expectedUpdatedReplicas = max(0, desiredReplicas-*rollingUpdate.Partition)
		}
		return statefulSet.Status.UpdatedReplicas < expectedUpdatedReplicas
	}
	return !wl.Updated(wl.GetGeneration())
}

// evictionWorkload reads the workload from the informer cache. A disabled kind has no informer and is
// read from the API.
func evictionWorkload(ctx context.Context, key WorkloadKey) (k8sapi.Workload, error) {
	if !managerutil.GetEnv(ctx).EnabledWorkloadKinds.Contains(key.Kind) {
		return k8sapi.GetWorkload(ctx, key.Name, key.Namespace, key.Kind)
	}
	return agentmap.GetWorkload(ctx, key.Name, key.Namespace, key.Kind)
}

func retryEvictPod(ctx context.Context, wl k8sapi.Workload, pod *core.Pod, replicas int) error {
	err := waitForReplicaCount(ctx, wl, replicas)
	if err != nil {
		clog.Error(ctx, err)
		return err
	}
	for {
		_, err = evictPod(ctx, pod)
		if err == nil || !errors.As(err, &disruptionBudgetError{}) {
			clog.Error(ctx, err)
			return err
		}
		delay := 2 * time.Second
		clog.Debugf(ctx, "%v. Will retry in %s", err, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// generateRestartAnnotationPatch generates a JSON patch that adds or updates the annotation
// We need to use this particular patch type because argo-rollouts do not support strategic merge patches.
func generateRestartAnnotationPatch(anns map[string]string) string {
	basePointer := "/spec/template/metadata/annotations"
	pointer := fmt.Sprintf(
		basePointer+"/%s",
		strings.ReplaceAll(annotation.RestartedAt, "/", "~1"),
	)

	if _, ok := anns[annotation.RestartedAt]; ok {
		return fmt.Sprintf(
			`[{"op": "replace", "path": "%s", "value": "%s"}]`, pointer, time.Now().Format(time.RFC3339),
		)
	}

	if len(anns) == 0 {
		return fmt.Sprintf(
			`[{"op": "add", "path": "%s", "value": {}}, {"op": "add", "path": "%s", "value": "%s"}]`, basePointer, pointer, time.Now().Format(time.RFC3339),
		)
	}

	return fmt.Sprintf(
		`[{"op": "add", "path": "%s", "value": "%s"}]`, pointer, time.Now().Format(time.RFC3339),
	)
}

func waitForReplicaCount(ctx context.Context, wl k8sapi.Workload, count int) error {
	for {
		pods, err := workloadPods(ctx, wl)
		if err != nil {
			return err
		}
		if len(pods) == count && !slices.ContainsFunc(pods, func(pod *core.Pod) bool { return pod.Status.Phase != core.PodRunning }) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s never scaled to %d", wl, count)
		case <-time.After(2 * time.Second):
		}
	}
}

func scaleIt(ctx context.Context, wl k8sapi.Workload, replicas int) error {
	clog.Debugf(ctx, "Scaling %s to %d replicas", wl, replicas)
	patch := fmt.Sprintf(`{"spec": {"replicas": %d}}`, replicas)
	err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(patch))
	if err != nil {
		err = fmt.Errorf("unable to scale %s to %d: %v", wl, replicas, err)
	}
	return err
}

func triggerScalingEviction(ctx context.Context, wl k8sapi.Workload, pod *core.Pod) (err error) {
	// Rollout of a replicatset/statefulset will not recreate the pods. For that to happen, the
	// set must be scaled up to replicas+1 and then the pod must be evicted before the replicas
	// are scaled back down.
	replicas := wl.Replicas()
	if err := scaleIt(ctx, wl, replicas+1); err != nil {
		return err
	}
	defer func() {
		if restoreErr := scaleIt(context.WithoutCancel(ctx), wl, replicas); restoreErr != nil {
			err = errors.Join(err, restoreErr)
		}
	}()
	return retryEvictPod(ctx, wl, pod, replicas+1)
}

func evictPod(ctx context.Context, pod *core.Pod) (bool, error) {
	clog.Debugf(ctx, "Attempting to evict pod %s", pod.Name)
	uid := pod.UID
	err := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(pod.Namespace).EvictV1(ctx, &v1.Eviction{
		ObjectMeta: meta.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
		DeleteOptions: &meta.DeleteOptions{
			Preconditions: &meta.Preconditions{UID: &uid},
		},
	})
	if err == nil || k8sErrors.IsNotFound(err) {
		store := informer.GetK8sFactory(ctx, pod.Namespace).Core().V1().Pods().Informer().GetStore()
		if cached, exists, getErr := store.Get(pod); getErr == nil && exists {
			if cachedPod, ok := cached.(*core.Pod); ok && cachedPod.UID == pod.UID {
				_ = store.Delete(cachedPod)
			}
		}
		clog.Debugf(ctx, "Pod %s was evicted or already replaced", pod.Name)
		return err == nil, nil
	}
	if k8sErrors.IsConflict(err) {
		// The name now belongs to a different UID. Leave the informer entry intact so the
		// replacement remains visible to subsequent reconciliation.
		clog.Debugf(ctx, "Pod %s was already replaced", pod.Name)
		return false, nil
	}
	if strings.Contains(err.Error(), "disruption budget") {
		err = disruptionBudgetError{error: err, podName: pod.Name}
	}
	return false, err
}

func podIsPendingOrRunning(pod *core.Pod) bool {
	switch pod.Status.Phase {
	case core.PodPending, core.PodRunning:
		return true
	default:
		return false
	}
}

type podLister interface {
	List(selector labels.Selector) (ret []*core.Pod, err error)
}

func getPodLister(ctx context.Context, namespace string) (lister podLister) {
	api := informer.GetK8sFactory(ctx, namespace).Core().V1().Pods().Lister()
	if namespace == "" {
		lister = api
	} else {
		lister = api.Pods(namespace)
	}
	return lister
}

func podList(ctx context.Context, namespace string) (wlPodMap, error) {
	lister := getPodLister(ctx, namespace)
	pods, err := lister.List(labels.Everything())
	if err != nil {
		return nil, fmt.Errorf("error listing pods %s: %v", whereWeWatch(namespace), err)
	}
	enabledWorkloads := managerutil.GetEnv(ctx).EnabledWorkloadKinds
	podMap := make(wlPodMap)
	for _, pod := range pods {
		if !podIsPendingOrRunning(pod) {
			continue
		}
		wl, err := podOwnerWorkload(ctx, pod, enabledWorkloads)
		if err != nil {
			if k8sErrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		podMap.add(wl, pod)
	}
	return podMap, nil
}

func podOwnerWorkload(ctx context.Context, pod *core.Pod, enabledWorkloads k8sapi.Kinds) (k8sapi.Workload, error) {
	if podKind, ok := pod.Labels[agentconfig.WorkloadKindLabel]; ok {
		if !enabledWorkloads.Contains(k8sapi.Kind(podKind)) {
			// Pod's label indicates a workload kind that has been disabled. As such, it will not be present in the
			// shared informer cache.
			return k8sapi.GetWorkload(ctx, pod.Labels[agentconfig.WorkloadNameLabel], pod.Namespace, k8sapi.Kind(podKind))
		}
		return agentmap.FindOwnerWorkload(ctx, k8sapi.Pod(pod), enabledWorkloads)
	}
	if enabledWorkloads.Contains(k8sapi.RolloutKind) && !enabledWorkloads.Contains(k8sapi.ReplicaSetKind) {
		// Rollout pods are owned by ReplicaSets even when ReplicaSets are not enabled as workloads. Resolve
		// through that intermediary without treating standalone ReplicaSets as eligible.
		for _, ref := range pod.OwnerReferences {
			if ref.Controller == nil || !*ref.Controller || ref.Kind != string(k8sapi.ReplicaSetKind) {
				continue
			}
			rs, err := k8sapi.GetWorkload(ctx, ref.Name, pod.Namespace, k8sapi.ReplicaSetKind)
			if err == nil {
				if owner, err := agentmap.FindOwnerWorkload(ctx, rs, enabledWorkloads); err == nil &&
					enabledWorkloads.Contains(owner.GetKind()) {
					return owner, nil
				}
			}
			break
		}
	}
	return agentmap.FindOwnerWorkload(ctx, k8sapi.Pod(pod), enabledWorkloads)
}

func workloadPods(ctx context.Context, wl k8sapi.Workload) ([]*core.Pod, error) {
	lister := getPodLister(ctx, wl.GetNamespace())
	selector, err := wl.Selector()
	if err != nil {
		return nil, err
	}
	pods, err := lister.List(selector)
	if err != nil {
		return nil, err
	}
	// A selector is not always unique to one workload. Stable and canary
	// Deployments commonly share one and distinguish their pods only by
	// template labels, so verify ownership before evicting a candidate pod.
	enabledWorkloads := managerutil.GetEnv(ctx).EnabledWorkloadKinds
	workloadKey := workloadKeyOf(wl)
	ownedPods := make([]*core.Pod, 0, len(pods))
	for _, pod := range pods {
		if !podIsPendingOrRunning(pod) {
			continue
		}
		owner, err := podOwnerWorkload(ctx, pod, enabledWorkloads)
		if err != nil {
			if k8sErrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if workloadKeyOf(owner) == workloadKey {
			ownedPods = append(ownedPods, pod)
		}
	}
	return ownedPods, nil
}

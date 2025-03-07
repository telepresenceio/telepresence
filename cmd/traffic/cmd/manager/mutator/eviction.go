package mutator

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	core "k8s.io/api/core/v1"
	v1 "k8s.io/api/policy/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"github.com/datawire/dlib/derror"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type wlPods struct {
	wl   k8sapi.Workload
	pods []*core.Pod
}

type wlPodMap map[workloadKey]*wlPods

func (em wlPodMap) add(wl k8sapi.Workload, pod *core.Pod) {
	k := workloadKey{kind: wl.GetKind(), name: wl.GetName(), namespace: wl.GetNamespace()}
	if v, ok := em[k]; ok {
		v.pods = append(v.pods, pod)
	} else {
		em[k] = &wlPods{wl: wl, pods: []*core.Pod{pod}}
	}
}

func (c *configWatcher) EvictPodsWithAgentConfigMismatch(ctx context.Context, wl k8sapi.Workload, scx agentconfig.SidecarExt) error {
	cfgJSON, err := agentconfig.MarshalTight(scx)
	if err != nil {
		return err
	}
	pods, err := workloadPods(ctx, wl)
	if err != nil {
		return err
	}
	return c.evictPodsWithAgentConfigMismatch(ctx, wl, pods, cfgJSON)
}

func (c *configWatcher) EvictPodsWithAgentConfig(ctx context.Context, wl k8sapi.Workload) error {
	pods, err := workloadPods(ctx, wl)
	if err != nil {
		return err
	}
	return c.evictPodsWithAgentConfigMismatch(ctx, wl, pods, "")
}

func (c *configWatcher) EvictAllPodsWithAgentConfig(ctx context.Context, namespace string) error {
	c.agentConfigs.Delete(namespace)
	evictMap, err := podList(ctx, namespace)
	if err != nil {
		return err
	}
	var errs derror.MultiError
	for _, wp := range evictMap {
		err = c.evictPodsWithAgentConfigMismatch(ctx, wp.wl, wp.pods, "")
		if err != nil {
			errs = append(errs, err)
		}
	}
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		return errs
	}
}

func (c *configWatcher) evictPodsWithAgentConfigMismatch(ctx context.Context, wl k8sapi.Workload, pods []*core.Pod, cfgJSON string) error {
	pods = slices.DeleteFunc(pods, func(pod *core.Pod) bool {
		if pod.Annotations[agentconfig.ConfigAnnotation] == cfgJSON {
			dlog.Tracef(ctx, "Keeping pod %s because its config is still valid", pod.Name)
			return true
		}
		return false
	})
	return c.evictPods(ctx, wl, pods)
}

func (c *configWatcher) evictPods(ctx context.Context, wl k8sapi.Workload, pods []*core.Pod) (err error) {
	didRollout := false
	counter := 0
	for _, pod := range pods {
		podID := pod.UID
		if c.isEvicted(podID) {
			dlog.Debugf(ctx, "Skipping pod %s because it is already deleted", pod.Name)
			continue
		}
		v := agentconfig.GetAnnotation(ctx, pod.ObjectMeta.Annotations, agentconfig.ManualInjectAnnotation, agentconfig.LegacyManualInjectAnnotation)
		if v == "true" {
			dlog.Tracef(ctx, "Skipping pod %s because it is managed manually", pod.Name)
			continue
		}
		c.inactivePods.Compute(podID, func(v inactivation, loaded bool) (inactivation, bool) {
			if loaded && v.deleted {
				return v, false
			}
			if !didRollout {
				didRollout, err = evictOrRollout(ctx, wl, pod, counter)
				if err != nil {
					return v, false
				}
				counter++
			}
			return inactivation{Time: time.Now(), deleted: true}, false
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func evictOrRollout(ctx context.Context, wl k8sapi.Workload, pod *core.Pod, counter int) (didRollout bool, err error) {
	err = evictPod(ctx, pod)
	if err == nil {
		return false, nil
	}
	if wl == nil || !strings.Contains(err.Error(), "disruption budget") {
		return false, fmt.Errorf("failed to evict pod %s: %v", pod.Name, err)
	}
	dlog.Debugf(ctx, "Unable to evict pod %s because it would violate the pod's disruption budget", pod.Name)
	if counter > 0 {
		// Other pod siblings were evicted successfully, which means that an engagement will be able to
		// proceed, Wait for the previous eviction(s) to trigger pod recreation, so the disruption budget
		// can be satisfied even though this pod is evicted.
		go func() {
			evictCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), managerutil.GetEnv(ctx).AgentArrivalTimeout)
			defer cancel()
			if err := retryEvictPod(evictCtx, wl, pod, wl.Replicas()); err != nil {
				dlog.Errorf(ctx, "unable to evict pod %s: %v", pod.Name, err)
			}
		}()
		return false, nil
	}
	switch wl.GetKind() {
	case k8sapi.StatefulSetKind, k8sapi.ReplicaSetKind:
		return false, triggerScalingEviction(ctx, wl, pod)
	default:
		dlog.Debugf(ctx, "Patching %s to trigger pod recreation", wl)
		restartAnnotation := generateRestartAnnotationPatch(wl.GetPodTemplate().Annotations)
		if err = wl.Patch(ctx, types.JSONPatchType, []byte(restartAnnotation)); err != nil {
			return false, fmt.Errorf("unable to patch %s: %v", wl, err)
		}
		dlog.Debugf(ctx, "Successfully patched %s", wl)
	}
	// Rollout applies to all pods for the workload, so we're done here
	return true, nil
}

func retryEvictPod(ctx context.Context, wl k8sapi.Workload, pod *core.Pod, replicas int) error {
	err := waitForReplicaCount(ctx, wl, replicas)
	if err != nil {
		return err
	}
	for {
		err = evictPod(ctx, pod)
		if err == nil || !strings.Contains(err.Error(), "disruption budget") {
			return err
		}
		delay := 2 * time.Second
		dlog.Debugf(ctx, "Unable to evict pod %s. Will retry in %s", pod.Name, delay)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// generateRestartAnnotationPatch generates a JSON patch that adds or updates the annotation
// We need to use this particular patch type because argo-rollouts do not support strategic merge patches.
func generateRestartAnnotationPatch(annotations map[string]string) string {
	basePointer := "/spec/template/metadata/annotations"
	pointer := fmt.Sprintf(
		basePointer+"/%s",
		strings.ReplaceAll(agentconfig.RestartedAtAnnotation, "/", "~1"),
	)

	if _, ok := annotations[agentconfig.RestartedAtAnnotation]; ok {
		return fmt.Sprintf(
			`[{"op": "replace", "path": "%s", "value": "%s"}]`, pointer, time.Now().Format(time.RFC3339),
		)
	}

	if len(annotations) == 0 {
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
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func scaleIt(ctx context.Context, wl k8sapi.Workload, replicas int) error {
	dlog.Debugf(ctx, "Scaling %s to %d replicas", wl, replicas)
	patch := fmt.Sprintf(`{"spec": {"replicas": %d}}`, replicas)
	err := wl.Patch(ctx, types.StrategicMergePatchType, []byte(patch))
	if err != nil {
		err = fmt.Errorf("unable to scale %s to %d: %v", wl, replicas, err)
	}
	return err
}

func triggerScalingEviction(ctx context.Context, wl k8sapi.Workload, pod *core.Pod) error {
	// Rollout of a replicatset/statefulset will not recreate the pods. In order for that to happen, the
	// set must be scaled down to zero replicas and then up again.
	replicas := wl.Replicas()
	if err := scaleIt(ctx, wl, replicas+1); err != nil {
		return err
	}
	defer func() {
		// Ensure that the original replica count is restored. Don't wait for it though
		go func() {
			if err := scaleIt(context.WithoutCancel(ctx), wl, replicas); err != nil {
				dlog.Error(ctx, err)
			}
		}()
	}()
	return retryEvictPod(ctx, wl, pod, replicas+1)
}

func evictPod(ctx context.Context, pod *core.Pod) error {
	err := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(pod.Namespace).EvictV1(ctx, &v1.Eviction{
		ObjectMeta: meta.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
	})
	if err == nil {
		store := informer.GetK8sFactory(ctx, pod.Namespace).Core().V1().Pods().Informer().GetStore()
		_ = store.Delete(pod)
		dlog.Debugf(ctx, "Successfully evicted pod %s", pod.Name)
	}
	return err
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
		var wl k8sapi.Workload
		if podKind, ok := pod.Labels[agentconfig.WorkloadKindLabel]; ok && !enabledWorkloads.Contains(k8sapi.Kind(podKind)) {
			// Pod's label indicates a workload kind that has been disabled. As such, it will not be present in the
			// shared informer cache.
			wl, err = k8sapi.GetWorkload(ctx, pod.Labels[agentconfig.WorkloadNameLabel], pod.Namespace, k8sapi.Kind(podKind))
		} else {
			wl, err = agentmap.FindOwnerWorkload(ctx, k8sapi.Pod(pod), enabledWorkloads)
		}
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
	return slices.DeleteFunc(slices.Clone(pods), func(pod *core.Pod) bool { return !podIsPendingOrRunning(pod) }), nil
}

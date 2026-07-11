package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const (
	// nodeAgentPodWatchDebounce coalesces a burst of pod events (e.g. a
	// rollout replacing every pod at once) into a single reconcile.
	nodeAgentPodWatchDebounce = time.Second

	// nodeAgentPodWatchResync is the reconcile interval that runs regardless
	// of pod events, covering a watch that stalls without ever closing its
	// channel.
	nodeAgentPodWatchResync = 30 * time.Second

	// nodeAgentPodWatchBackoff is the pause before retrying a list or watch
	// call that failed.
	nodeAgentPodWatchBackoff = 5 * time.Second
)

// nodeAgentWatchTimings carries the pod-set watcher's intervals. The zero
// value selects the production constants; a non-zero field overrides its
// constant, so a test can shrink the intervals on its own State without
// mutating anything shared.
type nodeAgentWatchTimings struct {
	debounce time.Duration
	resync   time.Duration
	backoff  time.Duration
}

// watchTimings returns s.nodeAgentWatchTimings with every zero field replaced
// by its production constant.
func (s *State) watchTimings() nodeAgentWatchTimings {
	t := s.nodeAgentWatchTimings
	if t.debounce == 0 {
		t.debounce = nodeAgentPodWatchDebounce
	}
	if t.resync == 0 {
		t.resync = nodeAgentPodWatchResync
	}
	if t.backoff == 0 {
		t.backoff = nodeAgentPodWatchBackoff
	}
	return t
}

// nodeAgentWatchKey identifies the node-agent pod-set watcher for one
// workload.
type nodeAgentWatchKey struct {
	name      string
	namespace string
}

// startNodeAgentPodWatch idempotently starts the goroutine that keeps the
// node-agent Job set for the workload identified by name and namespace
// congruent with its live pod set for as long as nodeAgentWanted holds. It
// must be called only after the claim it is started for is observable
// through nodeAgentWanted (the intercept stored, or the lease taken), since
// the watcher exits as soon as that predicate turns false.
func (s *State) startNodeAgentPodWatch(name, namespace string) {
	key := nodeAgentWatchKey{name: name, namespace: namespace}
	s.nodeAgentPodWatchers.LoadOrCompute(key, func() (struct{}, bool) {
		go s.nodeAgentPodWatchLoop(key)
		return struct{}{}, false
	})
}

// nodeAgentPodWatchLoop reconciles the node-agent Job set for key's workload
// and then watches its pods for further changes, repeating for as long as
// nodeAgentWanted holds. It removes its own map entry and returns as soon as
// nodeAgentWanted turns false or the background context ends, and never
// reaps a Job itself on either exit: that stays owned by the reap finalizer,
// ReleaseAgent, and the sweep in reconcileNodeAgentJobs, so a watcher that
// stops for an unrelated reason (a manager restart, say) cannot tear down a
// Job that another path still considers wanted -- the sweep's targetPod
// existence check is the backstop for that case.
func (s *State) nodeAgentPodWatchLoop(key nodeAgentWatchKey) {
	ctx := s.backgroundCtx
	defer func() {
		s.nodeAgentPodWatchers.Delete(key)
		// A claim created between this watcher's last nodeAgentWanted check
		// and the Delete above found the map entry still present and did not
		// start a watcher of its own; hand over to a fresh one so that claim
		// is not left without churn handling.
		if ctx.Err() == nil && s.nodeAgentWanted(key.name, key.namespace) {
			s.startNodeAgentPodWatch(key.name, key.namespace)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !s.nodeAgentWanted(key.name, key.namespace) {
			return
		}
		if err := s.reconcileNodeAgentPodSet(ctx, key.name, key.namespace); err != nil {
			clog.Errorf(ctx, "node-agent pod-set reconcile for %s.%s: %v", key.name, key.namespace, err)
		}
		if err := s.watchNodeAgentPods(ctx, key); err != nil {
			clog.Debugf(ctx, "node-agent pod watch for %s.%s: %v", key.name, key.namespace, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.watchTimings().backoff):
			}
		}
	}
}

// watchNodeAgentPods opens a live List+Watch on the workload's pods -- the
// shared pod informer is unusable here, for the same reason documented on
// nodeAgentTargets: the mutator's transform on it strips Status.Conditions
// and reduces ContainerStatuses -- and reconciles the Job set on every
// debounced burst of pod events, and again every nodeAgentPodWatchResync
// regardless of events. It returns nil once the claim ends or ctx is done,
// and a non-nil error for a list/watch failure or a closed channel, which the
// caller retries after a backoff.
func (s *State) watchNodeAgentPods(ctx context.Context, key nodeAgentWatchKey) error {
	wl, err := agentmap.GetWorkload(ctx, key.name, key.namespace, "")
	if err != nil {
		return fmt.Errorf("unable to resolve workload %s.%s: %w", key.name, key.namespace, err)
	}
	selector, err := wl.Selector()
	if err != nil {
		return fmt.Errorf("unable to get pod selector for %s: %w", wl, err)
	}
	w, err := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(key.namespace).Watch(ctx, meta.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return fmt.Errorf("unable to watch pods for %s: %w", wl, err)
	}
	defer w.Stop()

	timings := s.watchTimings()
	resync := time.NewTicker(timings.resync)
	defer resync.Stop()

	var debounce *time.Timer
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()

	for {
		var debounceCh <-chan time.Time
		if debounce != nil {
			debounceCh = debounce.C
		}
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-w.ResultChan():
			if !ok {
				return errors.New("watch channel closed")
			}
			if debounce == nil {
				debounce = time.NewTimer(timings.debounce)
			} else {
				if !debounce.Stop() {
					select {
					case <-debounce.C:
					default:
					}
				}
				debounce.Reset(timings.debounce)
			}
		case <-debounceCh:
			debounce = nil
			if !s.nodeAgentWanted(key.name, key.namespace) {
				return nil
			}
			if err := s.reconcileNodeAgentPodSet(ctx, key.name, key.namespace); err != nil {
				clog.Errorf(ctx, "node-agent pod-set reconcile for %s.%s: %v", key.name, key.namespace, err)
			}
		case <-resync.C:
			if !s.nodeAgentWanted(key.name, key.namespace) {
				return nil
			}
			if err := s.reconcileNodeAgentPodSet(ctx, key.name, key.namespace); err != nil {
				clog.Errorf(ctx, "node-agent pod-set resync for %s.%s: %v", key.name, key.namespace, err)
			}
		}
	}
}

// reconcileNodeAgentPodSet computes and applies one create/delete pass of the
// node-agent Job set for the workload identified by name and namespace. A
// nodeAgentTargets error (no admissible pod) is logged and treated as no
// targets rather than failing the pass, so that Jobs of pods that are truly
// gone still get deleted during a rollout window where every current pod is
// momentarily inadmissible.
func (s *State) reconcileNodeAgentPodSet(ctx context.Context, name, namespace string) error {
	wl, err := agentmap.GetWorkload(ctx, name, namespace, "")
	if err != nil {
		return fmt.Errorf("unable to resolve workload %s.%s: %w", name, namespace, err)
	}

	targets, err := nodeAgentTargets(ctx, wl)
	if err != nil {
		clog.Infof(ctx, "node-agent pod-set reconcile for %s: no admissible target: %v", wl, err)
		targets = nil
	}

	livePodNames, err := liveWorkloadPodNames(ctx, wl)
	if err != nil {
		return err
	}

	jobs, err := nodeAgentJobsForWorkload(ctx, name, namespace)
	if err != nil {
		return err
	}

	hasInterceptClaim := s.findLiveNodeAgentIntercept(name, namespace, "") != nil
	createFor, deleteJobs := nodeAgentJobDiff(targets, livePodNames, jobs, hasInterceptClaim)

	if len(createFor) > 0 && !s.nodeAgentWanted(name, namespace) {
		// The claim ended mid-reconcile; the orphan sweep is the backstop for
		// this residual race, so no Job is created on its way out.
		createFor = nil
	}

	env := managerutil.GetEnv(ctx)
	mgrNs := env.ManagerNamespace
	jobsClient := k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(mgrNs)

	if len(createFor) > 0 {
		cfg, err := s.GetOrGenerateAgentConfig(ctx, name, namespace)
		if err != nil {
			return err
		}
		for _, t := range createFor {
			// This reconciler only runs while a claim exists, so an
			// image-change replacement here would tear down the data path
			// of every live intercept or ingest sharing the target's Job;
			// defer it to the next time the Job set turns over on its own.
			if err := ensureNodeAgentTarget(ctx, jobsClient, mgrNs, env.NodeAgentCRISocket, cfg, t, false); err != nil {
				clog.Errorf(ctx, "unable to create node-agent job for %s pod %s: %v", wl, t.podName, err)
			}
		}
	}

	if len(deleteJobs) > 0 {
		// Foreground propagation: see reapNodeAgentJobs -- a successor Job
		// for the same target pod must never program the shared nft table
		// while a predecessor's pod can still tear it down.
		propagation := meta.DeletePropagationForeground
		for _, jobName := range deleteJobs {
			if err := jobsClient.Delete(ctx, jobName, meta.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !k8sErrors.IsNotFound(err) {
				clog.Errorf(ctx, "unable to reap node-agent job %s for %s: %v", jobName, wl, err)
			}
		}
	}
	return nil
}

// liveWorkloadPodNames returns the name of every pod of wl that is not
// already terminating (no DeletionTimestamp), independent of admissibility or
// readiness. nodeAgentJobDiff needs this full set to tell a pod that is
// merely unready or temporarily inadmissible -- whose Job must be kept --
// apart from one that is genuinely gone, which nodeAgentTargets' admissible-
// only result cannot.
func liveWorkloadPodNames(ctx context.Context, wl k8sapi.Workload) (map[string]struct{}, error) {
	selector, err := wl.Selector()
	if err != nil {
		return nil, err
	}
	pods, err := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(wl.GetNamespace()).List(ctx, meta.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("unable to list pods for %s: %w", wl, err)
	}
	names := make(map[string]struct{}, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp == nil {
			names[pod.Name] = struct{}{}
		}
	}
	return names, nil
}

// nodeAgentJobDiff computes the create/delete diff for one reconcile pass.
// targets are the workload's current admissible Running & Ready pods
// (nodeAgentTargets). livePodNames are the names of every pod of the
// workload that is not itself terminating -- admissible or not, ready or not
// -- the set a Job's target pod is checked against before it is reaped, so a
// merely unready or temporarily inadmissible pod never loses its Job. jobs
// are the workload's current non-terminating node-agent Jobs.
// hasInterceptClaim selects between the two desired-state policies: a live
// node-agent intercept wants a Job on every target; a lease-only (ingest)
// claim wants exactly one live Job on a still-admissible target and never
// more, since an ingest reads env and mounts from a single pod.
func nodeAgentJobDiff(
	targets []nodeAgentPodTarget,
	livePodNames map[string]struct{},
	jobs []nodeAgentJobTarget,
	hasInterceptClaim bool,
) (createFor []nodeAgentPodTarget, deleteJobs []string) {
	haveJob := make(map[string]struct{}, len(jobs))
	for _, j := range jobs {
		haveJob[j.podName] = struct{}{}
	}

	if hasInterceptClaim {
		for _, t := range targets {
			if _, ok := haveJob[t.podName]; !ok {
				createFor = append(createFor, t)
			}
		}
	} else if len(targets) > 0 {
		admissible := make(map[string]struct{}, len(targets))
		for _, t := range targets {
			admissible[t.podName] = struct{}{}
		}
		liveTarget := false
		for _, j := range jobs {
			if _, ok := admissible[j.podName]; ok {
				liveTarget = true
				break
			}
		}
		if !liveTarget {
			createFor = append(createFor, targets[0])
		}
	}

	for _, j := range jobs {
		if _, ok := livePodNames[j.podName]; !ok {
			deleteJobs = append(deleteJobs, j.jobName)
		}
	}
	return createFor, deleteJobs
}

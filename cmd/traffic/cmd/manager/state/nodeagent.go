package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	batchClientV1 "k8s.io/client-go/kubernetes/typed/batch/v1"

	"github.com/telepresenceio/clog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const (
	// nodeAgentContainerName is the name of the sole container in a
	// node-agent Job's pod template.
	nodeAgentContainerName = "traffic-node-agent"

	// nodeAgentCRIVolumeName is the name of the hostPath volume that mounts
	// the container-runtime socket into the node-agent container.
	nodeAgentCRIVolumeName = "cri-socket"

	// nodeAgentAppLabel and nodeAgentAppLabelValue identify a node-agent Job
	// (and its pod template) so that a later change can find and reap them.
	nodeAgentAppLabel      = "app"
	nodeAgentAppLabelValue = "traffic-node-agent"

	// nodeAgentNameLabel carries the name of the traffic-agent instance
	// (agentconfig.Sidecar.AgentName) that the Job was created for.
	nodeAgentNameLabel = "telepresence.io/agentName"

	// nodeAgentNamespaceLabel carries the namespace of the workload
	// (agentconfig.Sidecar.Namespace) that the Job was created for.
	nodeAgentNamespaceLabel = "telepresence.io/workloadNamespace"

	// nodeAgentTargetPodLabel carries the name of the pod that the node-agent
	// Job targets.
	nodeAgentTargetPodLabel = "telepresence.io/targetPod"

	// nodeAgentTTLSecondsAfterFinished is how long a completed or failed
	// node-agent Job is kept around before being garbage collected.
	nodeAgentTTLSecondsAfterFinished = int32(300)

	// nodeAgentReconcileInterval is how often orphaned node-agent Jobs are
	// swept.
	nodeAgentReconcileInterval = time.Minute

	// nodeAgentReconcileStartupDelay lets clients reconnect and restore their
	// intercepts after a manager restart before the first sweep, so a Job whose
	// intercept is about to be restored is not reaped prematurely.
	nodeAgentReconcileStartupDelay = 2 * time.Minute

	// nodeAgentOrphanGracePeriod is the minimum age a Job must reach before the
	// sweep may reap it, covering the window between PrepareIntercept (which
	// creates the Job) and AddIntercept (which registers the reap finalizer).
	nodeAgentOrphanGracePeriod = 90 * time.Second
)

// nodeAgentGateErr returns a user error when node-agent mode has been
// requested for an intercept but the traffic-manager does not have it
// enabled, and nil otherwise.
func nodeAgentGateErr(env *managerutil.Env) error {
	if !env.NodeAgentEnabled {
		return errcat.User.New("node-agent mode is not enabled on this traffic-manager")
	}
	return nil
}

// nodeAgentWorkloadSelector returns the label selector matching every
// node-agent Job created for the agent named agentName and the workload in
// namespace, regardless of which pod each one targets.
func nodeAgentWorkloadSelector(agentName, namespace string) string {
	return fmt.Sprintf("%s=%s,%s=%s,%s=%s",
		nodeAgentAppLabel, nodeAgentAppLabelValue,
		nodeAgentNameLabel, agentName,
		nodeAgentNamespaceLabel, namespace)
}

// reapNodeAgentJobs deletes the node-agent Job(s) created for the agent named
// agentName and the workload in namespace, in the traffic-manager's own
// namespace. It is invoked as an intercept finalizer, so it runs both on
// explicit intercept removal and on client-session drop.
//
// NOTE: this deletes by the agentName and workloadNamespace labels, so it
// reaps the Job for every node-agent intercept of that agent in that
// namespace at once. That is correct while node-agent intercepts are 1:1
// with the agent; supporting multiple concurrent node-agent intercepts
// sharing one Job is future work.
func reapNodeAgentJobs(ctx context.Context, agentName, namespace string) error {
	env := managerutil.GetEnv(ctx)
	ns := env.ManagerNamespace
	sel := nodeAgentWorkloadSelector(agentName, namespace)
	// Foreground propagation keeps the Job object present (with a
	// DeletionTimestamp) until its pod is fully gone. A successor Job for
	// the same target must never program the shared nft table while a
	// predecessor's pod can still tear it down, and ensureNodeAgent's
	// AlreadyExists/staleness handling relies on seeing that terminating
	// Job -- and on replaceNodeAgentJob waiting for it to vanish entirely,
	// pod included -- before creating the replacement. Background
	// propagation would instead remove the Job object immediately while the
	// pod (and its teardown of that table) lingers, letting a successor
	// race ahead of it. This does not change how long the delete takes to
	// complete -- DeleteCollection does not block on foreground propagation
	// -- only the ordering guarantee it gives callers that observe the Job.
	propagation := meta.DeletePropagationForeground
	err := k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(ns).DeleteCollection(ctx,
		meta.DeleteOptions{PropagationPolicy: &propagation},
		meta.ListOptions{LabelSelector: sel})
	if err != nil && !k8sErrors.IsNotFound(err) {
		return fmt.Errorf("unable to reap node-agent job(s) for agent %s.%s: %w", agentName, namespace, err)
	}
	clog.Debugf(ctx, "reaped node-agent job(s) for agent %s.%s", agentName, namespace)
	return nil
}

// ReapAllNodeAgentJobs deletes every node-agent Job in the traffic-manager's
// own namespace, unconditionally: unlike reapNodeAgentJobs and
// reconcileNodeAgentJobs, it does not consult intercepts or leases, because
// it is invoked only when the traffic-manager itself is being uninstalled
// (via the pre-delete hook's /uninstall endpoint, the same trigger that
// makes the agent injector roll back every injected sidecar). Uninstall
// overrides every claim.
func ReapAllNodeAgentJobs(ctx context.Context) error {
	env := managerutil.GetEnv(ctx)
	ns := env.ManagerNamespace
	sel := fmt.Sprintf("%s=%s", nodeAgentAppLabel, nodeAgentAppLabelValue)
	// Foreground propagation is used here for the same reason as in
	// reapNodeAgentJobs: it keeps the ordering guarantee uniform even
	// though, at uninstall, there is no successor Job that could race a
	// predecessor's table teardown.
	propagation := meta.DeletePropagationForeground
	err := k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(ns).DeleteCollection(ctx,
		meta.DeleteOptions{PropagationPolicy: &propagation},
		meta.ListOptions{LabelSelector: sel})
	if err != nil && !k8sErrors.IsNotFound(err) {
		return fmt.Errorf("unable to reap node-agent jobs in %s: %w", ns, err)
	}
	clog.Debugf(ctx, "reaped all node-agent jobs in %s", ns)
	return nil
}

// reconcileNodeAgentJobsLoop periodically reaps node-agent Jobs that have no
// matching live intercept. It is the safety net for the two cases the
// per-intercept reap finalizer cannot cover: a client that creates a Job in
// PrepareIntercept but never reaches AddIntercept (so no finalizer is ever
// registered), and a manager restart that loses the in-memory finalizers. A
// node-agent Job never completes on its own, so ttlSecondsAfterFinished cannot
// substitute for this sweep.
func (s *State) reconcileNodeAgentJobsLoop(ctx context.Context) error {
	if !managerutil.GetEnv(ctx).NodeAgentEnabled {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(nodeAgentReconcileStartupDelay):
	}
	ticker := time.NewTicker(nodeAgentReconcileInterval)
	defer ticker.Stop()
	for {
		if err := s.reconcileNodeAgentJobs(ctx); err != nil {
			clog.Errorf(ctx, "node-agent job reconcile failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// reconcileNodeAgentJobs reaps every node-agent Job that nothing wants: it
// has neither a live node-agent intercept nor a lease (taken by
// EnsureAgent(node_agent=true), e.g. for an ingest) whose session is still
// alive. Jobs younger than nodeAgentOrphanGracePeriod are skipped. It also
// reaps an aged Job whose workload still has a live claim if the Job's target
// pod no longer exists: the per-workload pod-set watcher
// (nodeAgentPodWatchLoop) normally reaps that Job the moment the pod is gone,
// but a manager restart loses that in-memory watcher along with every other
// piece of runtime state, so this sweep is its backstop too.
func (s *State) reconcileNodeAgentJobs(ctx context.Context) error {
	ns := managerutil.GetEnv(ctx).ManagerNamespace
	sel := fmt.Sprintf("%s=%s", nodeAgentAppLabel, nodeAgentAppLabelValue)
	jobs, err := k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(ns).List(ctx, meta.ListOptions{LabelSelector: sel})
	if err != nil {
		return fmt.Errorf("unable to list node-agent jobs in %s: %w", ns, err)
	}

	type wantedKey struct {
		name      string
		namespace string
	}
	wanted := make(map[wantedKey]struct{})
	s.intercepts.Range(func(_ string, i *Intercept) bool {
		if i.Spec.GetNodeAgent() && i.Disposition != rpc.InterceptDispositionType_REMOVED {
			wanted[wantedKey{name: i.Spec.GetAgent(), namespace: i.Spec.GetNamespace()}] = struct{}{}
		}
		return true
	})
	s.leases.Range(func(k leaseKey, _ struct{}) bool {
		if _, ok := s.clients.Load(k.sessionID); ok {
			wanted[wantedKey{name: k.name, namespace: k.namespace}] = struct{}{}
		}
		return true
	})

	// See the comment on the propagation policy in reapNodeAgentJobs above:
	// the same successor/teardown ordering guarantee applies here.
	propagation := meta.DeletePropagationForeground
	now := time.Now()
	for i := range jobs.Items {
		job := &jobs.Items[i]
		// A Job created by an older build lacks the workloadNamespace label,
		// so its key's namespace is "" and never matches a wanted key; such a
		// Job is reaped once it passes the grace period below.
		key := wantedKey{name: job.Labels[nodeAgentNameLabel], namespace: job.Labels[nodeAgentNamespaceLabel]}
		_, isWanted := wanted[key]
		if now.Sub(job.CreationTimestamp.Time) < nodeAgentOrphanGracePeriod {
			continue
		}
		reason := "no live intercept"
		if isWanted {
			gone, err := targetPodGone(ctx, key.namespace, job.Labels[nodeAgentTargetPodLabel])
			if err != nil {
				clog.Errorf(ctx, "unable to check target pod of node-agent job %s.%s: %v", job.Name, ns, err)
				continue
			}
			if !gone {
				continue
			}
			reason = "target pod no longer exists"
		}
		clog.Infof(ctx, "reaping node-agent job %s.%s (%s)", job.Name, ns, reason)
		if err := k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(ns).Delete(ctx, job.Name,
			meta.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !k8sErrors.IsNotFound(err) {
			clog.Errorf(ctx, "unable to reap node-agent job %s.%s: %v", job.Name, ns, err)
		}
	}
	return nil
}

// targetPodGone reports whether podName no longer exists in namespace. An
// empty podName (a Job created before the targetPod label existed) or an
// empty namespace is treated as "not gone" -- there is nothing to check, so
// the existing age-only sweep behavior applies instead.
func targetPodGone(ctx context.Context, namespace, podName string) (bool, error) {
	if podName == "" || namespace == "" {
		return false, nil
	}
	_, err := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(namespace).Get(ctx, podName, meta.GetOptions{})
	if err == nil {
		return false, nil
	}
	if k8sErrors.IsNotFound(err) {
		return true, nil
	}
	return false, err
}

// ensureNodeAgent provisions a node-hosted traffic-agent (a manager-created
// Job that enters the target pod's namespaces) for the workload, using the
// agent config that the caller already generated via a dryRun ensureAgent
// call. It creates only Jobs; it never touches the workload's pod template,
// so no sidecar is injected and no pod is restarted.
//
// allReplicas selects between the two provisioning policies: true (the
// intercept path) ensures one Job per admissible target, so that traffic
// load-balanced to any replica is intercepted; false (the ingest path, via
// EnsureAgent) reuses a live Job for a still-admissible target if one
// exists, and otherwise ensures a Job for a single target only, since an
// ingest reads env and mounts from one pod and gains nothing from the
// others.
func (s *State) ensureNodeAgent(
	ctx context.Context,
	wl k8sapi.Workload,
	cfg *agentconfig.Sidecar,
	allReplicas bool,
) error {
	env := managerutil.GetEnv(ctx)
	if env.NodeAgentCRISocket == "" {
		return errcat.User.New(
			"node-agent mode requires the container-runtime socket path (Helm value nodeAgent.criSocket), " +
				"which is unset on this traffic-manager")
	}

	targets, err := nodeAgentTargets(ctx, wl)
	if err != nil {
		return err
	}

	namespace := env.ManagerNamespace
	jobs := k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(namespace)

	if allReplicas {
		for _, target := range targets {
			if err := ensureNodeAgentTarget(ctx, jobs, namespace, env.NodeAgentCRISocket, cfg, target); err != nil {
				return err
			}
		}
		return nil
	}

	// A Job already targeting one of the current admissible pods still
	// serves the ingest; reuse it rather than creating a second Job for the
	// same workload.
	admissible := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		admissible[target.podName] = struct{}{}
	}
	sel := nodeAgentWorkloadSelector(cfg.AgentName, cfg.Namespace)
	existing, err := jobs.List(ctx, meta.ListOptions{LabelSelector: sel})
	if err != nil {
		return fmt.Errorf("unable to list node-agent jobs for %s.%s: %w", cfg.AgentName, cfg.Namespace, err)
	}
	for i := range existing.Items {
		job := &existing.Items[i]
		if job.DeletionTimestamp != nil {
			continue
		}
		if _, ok := admissible[job.Labels[nodeAgentTargetPodLabel]]; ok {
			clog.Debugf(ctx, "reusing existing node-agent job %s.%s for ingest", job.Name, namespace)
			return nil
		}
	}
	return ensureNodeAgentTarget(ctx, jobs, namespace, env.NodeAgentCRISocket, cfg, targets[0])
}

// ensureNodeAgentTarget ensures a single node-agent Job for target: the
// per-Job create/AlreadyExists/staleness logic, extracted from
// ensureNodeAgent so it runs unchanged whether it's applied to every target
// of a workload (an intercept) or to one (an ingest).
func ensureNodeAgentTarget(
	ctx context.Context,
	jobs batchClientV1.JobInterface,
	namespace, criSocket string,
	cfg *agentconfig.Sidecar,
	target nodeAgentPodTarget,
) error {
	for _, cn := range cfg.Containers {
		if _, ok := target.containerIDs[cn.Name]; !ok {
			return errcat.User.Newf("unable to resolve a container ID for container %q in pod %s.%s", cn.Name, target.podName, cfg.Namespace)
		}
	}

	job, err := buildNodeAgentJob(cfg, nodeAgentJobOpts{
		namespace:    namespace,
		nodeName:     target.nodeName,
		containerIDs: target.containerIDs,
		criSocket:    criSocket,
		podName:      target.podName,
		podIP:        target.podIP,
	})
	if err != nil {
		return err
	}

	if _, err = jobs.Create(ctx, job, meta.CreateOptions{}); err != nil {
		if !k8sErrors.IsAlreadyExists(err) {
			return fmt.Errorf("unable to create node-agent job %s.%s: %w", job.Name, namespace, err)
		}
		existing, getErr := jobs.Get(ctx, job.Name, meta.GetOptions{})
		if getErr != nil {
			if !k8sErrors.IsNotFound(getErr) {
				return fmt.Errorf("unable to get existing node-agent job %s.%s: %w", job.Name, namespace, getErr)
			}
			// The Job vanished between the Create and this Get; retry the
			// Create once.
			if _, err = jobs.Create(ctx, job, meta.CreateOptions{}); err != nil {
				return fmt.Errorf("unable to create node-agent job %s.%s: %w", job.Name, namespace, err)
			}
			return nil
		}
		if stale, reason := nodeAgentJobStale(existing, job); stale {
			clog.Infof(ctx, "replacing stale node-agent job %s.%s: %s", job.Name, namespace, reason)
			if err = replaceNodeAgentJob(ctx, jobs, job); err != nil {
				return err
			}
			return nil
		}
		// A Job with this deterministic name already exists for this agent
		// and target pod, and still reflects the same target; treat a
		// re-request as idempotent.
		clog.Debugf(ctx, "reusing existing node-agent job %s.%s", job.Name, namespace)
	}
	return nil
}

// nodeAgentJobStale reports whether existing no longer reflects the target
// that desired was built for, and if so, a short reason for logging.
// existing is replaced rather than reused when it is already terminating,
// when it has failed, or when its container image or target-identifying
// environment (the container IDs or pod IP the node-agent was given) differs
// from desired. Any other difference -- such as the marshaled agent config
// -- does not force a replace, so that a re-request for the same live target
// stays idempotent.
func nodeAgentJobStale(existing, desired *batchv1.Job) (bool, string) {
	if existing.DeletionTimestamp != nil {
		return true, "existing job is terminating"
	}
	for _, cond := range existing.Status.Conditions {
		if cond.Type == batchv1.JobFailed && cond.Status == core.ConditionTrue {
			return true, "existing job failed"
		}
	}
	existingContainer, ok := soleContainer(existing)
	if !ok {
		return true, "existing job has no container"
	}
	desiredContainer, ok := soleContainer(desired)
	if !ok {
		return true, "desired job has no container"
	}
	if existingContainer.Image != desiredContainer.Image {
		return true, fmt.Sprintf("image changed from %q to %q", existingContainer.Image, desiredContainer.Image)
	}
	for _, name := range []string{agentconfig.EnvNodeAgentContainerIDs, agentconfig.EnvNodeAgentPodIP} {
		if envVarValue(existingContainer.Env, name) != envVarValue(desiredContainer.Env, name) {
			return true, fmt.Sprintf("%s changed", name)
		}
	}
	return false, ""
}

// soleContainer returns the single container of job's pod template.
func soleContainer(job *batchv1.Job) (core.Container, bool) {
	cs := job.Spec.Template.Spec.Containers
	if len(cs) != 1 {
		return core.Container{}, false
	}
	return cs[0], true
}

// envVarValue returns the literal value of the named environment variable,
// or "" if it is absent or its value comes from a ValueFrom source.
func envVarValue(env []core.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

// replaceNodeAgentJob deletes the stale Job named job.Name with foreground
// propagation, so its pod is gone before the Job object disappears -- the
// new Job's agent must not race the old agent for the same listen ports
// inside the target pod's network namespace -- waits for it to vanish, and
// then creates job in its place.
func replaceNodeAgentJob(ctx context.Context, jobs batchClientV1.JobInterface, job *batchv1.Job) error {
	propagation := meta.DeletePropagationForeground
	if err := jobs.Delete(ctx, job.Name, meta.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !k8sErrors.IsNotFound(err) {
		return fmt.Errorf("unable to delete stale node-agent job %s: %w", job.Name, err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := jobs.Get(ctx, job.Name, meta.GetOptions{}); err != nil {
			if k8sErrors.IsNotFound(err) {
				break
			}
			return fmt.Errorf("unable to get node-agent job %s: %w", job.Name, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	if _, err := jobs.Create(ctx, job, meta.CreateOptions{}); err != nil {
		return fmt.Errorf("unable to create node-agent job %s: %w", job.Name, err)
	}
	return nil
}

// nodeAgentPodTarget is the per-pod result of nodeAgentTargets: the node the
// pod is scheduled on, a map of container name to CRI container ID (as
// reported by the kubelet, including the runtime scheme prefix, e.g.
// "containerd://abc123"; pkg/cri strips that prefix), the pod's name, and
// the pod's IP (which the node-agent needs as the PodIP of the netfilter
// ruleset it programs into the target's network namespace).
type nodeAgentPodTarget struct {
	nodeName     string
	containerIDs map[string]string
	podName      string
	podIP        string
}

// nodeAgentTargets returns one target for every Running & Ready pod of wl
// that passes checkNodeAgentTarget. A pod that fails
// the check is skipped with a log line rather than failing the whole
// request: replicas share a pod template, so in the steady state they pass
// or fail together, but during a rollout old-template pods (e.g. carrying a
// sidecar) can coexist with new ones, and skipping keeps the attachment
// usable through that window. An error is returned only when no admissible
// pod exists: the first check failure if at least one pod was Running &
// Ready (it names the precise cause), or the generic "no pod available"
// error if none was.
func nodeAgentTargets(ctx context.Context, wl k8sapi.Workload) ([]nodeAgentPodTarget, error) {
	selector, err := wl.Selector()
	if err != nil {
		return nil, err
	}
	// This must be a live apiserver list. The manager's shared pod informer
	// is not usable here: the mutator installs a transform on it (see
	// mutator/watcher.go startPods) that strips Status.Conditions and
	// reduces ContainerStatuses to their State, so a cached pod can never
	// satisfy podRunningAndReady and carries no ContainerID to resolve.
	pods, err := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(wl.GetNamespace()).List(ctx, meta.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("unable to list pods for %s: %w", wl, err)
	}

	var targets []nodeAgentPodTarget
	var readyCount int
	var checkErr error
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !podRunningAndReady(pod) {
			continue
		}
		readyCount++
		if pod.Status.PodIP == "" {
			// An invariant violation, not a normal admissibility failure:
			// log it but treat the pod like any other inadmissible one, so
			// it doesn't fail the request for the other replicas.
			clog.Infof(ctx, "skipping pod %s.%s as a node-agent target: no PodIP despite being running and ready", pod.Name, pod.Namespace)
			continue
		}
		if err := checkNodeAgentTarget(pod); err != nil {
			clog.Infof(ctx, "skipping pod %s.%s as a node-agent target: %v", pod.Name, pod.Namespace, err)
			if checkErr == nil {
				checkErr = err
			}
			continue
		}
		ids := make(map[string]string, len(pod.Status.ContainerStatuses))
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.ContainerID != "" {
				ids[cs.Name] = cs.ContainerID
			}
		}
		targets = append(targets, nodeAgentPodTarget{
			nodeName:     pod.Spec.NodeName,
			containerIDs: ids,
			podName:      pod.Name,
			podIP:        pod.Status.PodIP,
		})
	}
	if len(targets) == 0 {
		if readyCount > 0 && checkErr != nil {
			return nil, checkErr
		}
		return nil, errcat.User.Newf("%s has no running and ready pod available to host a node-agent", wl)
	}
	return targets, nil
}

// nodeAgentJobTarget identifies one non-terminating node-agent Job for a
// workload: its own name (node-agent Jobs generate their pod's name with
// this as prefix) and the workload pod it targets (the
// telepresence.io/targetPod label).
type nodeAgentJobTarget struct {
	jobName string
	podName string
}

// nodeAgentJobsForWorkload lists the non-terminating node-agent Jobs for the
// agent named agentName and the workload in namespace, in the traffic-
// manager's own namespace. The caller ensures the Jobs before listing them,
// so counting the actual Job set (rather than re-listing pods) keeps a wait
// for their agents consistent with what was really created.
func nodeAgentJobsForWorkload(ctx context.Context, agentName, namespace string) ([]nodeAgentJobTarget, error) {
	env := managerutil.GetEnv(ctx)
	sel := nodeAgentWorkloadSelector(agentName, namespace)
	jobs, err := k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(env.ManagerNamespace).List(ctx, meta.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("unable to list node-agent jobs for %s.%s: %w", agentName, namespace, err)
	}
	targets := make([]nodeAgentJobTarget, 0, len(jobs.Items))
	for i := range jobs.Items {
		job := &jobs.Items[i]
		if job.DeletionTimestamp != nil {
			continue
		}
		targets = append(targets, nodeAgentJobTarget{jobName: job.Name, podName: job.Labels[nodeAgentTargetPodLabel]})
	}
	return targets, nil
}

// checkNodeAgentTarget rejects a target pod that cannot host a node-agent: one
// on the host network (whose namespace the route controller also programs), one
// that already has an injected traffic-agent sidecar (whose network namespace
// and agent ports the node-agent would collide with), and one in its own user
// namespace (hostUsers:false), whose loop-prevention discriminator needs a
// packet mark that is not wired yet.
func checkNodeAgentTarget(pod *core.Pod) error {
	if pod.Spec.HostNetwork {
		return errcat.User.Newf(
			"pod %s.%s uses host networking, which node-agent mode does not support", pod.Name, pod.Namespace)
	}
	if pod.Spec.HostUsers != nil && !*pod.Spec.HostUsers {
		return errcat.User.Newf(
			"pod %s.%s runs in its own user namespace (hostUsers:false), which node-agent mode does not support yet",
			pod.Name, pod.Namespace)
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == agentconfig.ContainerName {
			return errcat.User.Newf(
				"pod %s.%s already has an injected traffic-agent; node-agent mode cannot be used with a sidecar agent",
				pod.Name, pod.Namespace)
		}
	}
	return nil
}

// podRunningAndReady reports whether pod is in the Running phase and its
// Ready condition is true.
func podRunningAndReady(pod *core.Pod) bool {
	if pod.Status.Phase != core.PodRunning {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == core.PodReady {
			return c.Status == core.ConditionTrue
		}
	}
	return false
}

// nodeAgentJobOpts carries the per-request values that parameterize a
// node-agent Job. cfg (the agentconfig.Sidecar) supplies the values that are
// shared with the sidecar traffic-agent.
type nodeAgentJobOpts struct {
	// namespace is where the Job is created: the traffic-manager's own
	// namespace, not necessarily the target pod's namespace.
	namespace string

	// nodeName is the node that the target pod is scheduled on. The Job's
	// pod is pinned to it via Spec.NodeName.
	nodeName string

	// containerIDs maps configured agent container name to CRI container ID
	// (including the runtime scheme prefix) of the target pod.
	containerIDs map[string]string

	// criSocket is the container-runtime socket path to mount into the
	// node-agent container. It must be non-empty; buildNodeAgentJob rejects
	// an empty value the same way it rejects the other required fields
	// below.
	criSocket string

	// podName is the name of the target pod.
	podName string

	// podIP is the IP address of the target pod. The node-agent programs its
	// netfilter ruleset with this as the PodIP, into the target's own network
	// namespace.
	podIP string
}

// buildNodeAgentJob returns a Job that runs a node-hosted traffic-agent
// configured by cfg, pinned to opts.nodeName, with the standard Kubernetes
// pod-security posture (hostPID, a narrow set of Linux capabilities, and a
// hostPath mount of the container-runtime socket) needed to read the target
// pod's process environment and filesystem from /proc and to program its
// network namespace. It performs no I/O and makes no API calls.
func buildNodeAgentJob(cfg *agentconfig.Sidecar, opts nodeAgentJobOpts) (*batchv1.Job, error) {
	if opts.nodeName == "" {
		return nil, errors.New("buildNodeAgentJob: no node name given")
	}
	if len(opts.containerIDs) == 0 {
		return nil, errors.New("buildNodeAgentJob: no container IDs given")
	}
	if opts.podIP == "" {
		return nil, errors.New("buildNodeAgentJob: no target pod IP given")
	}
	if opts.criSocket == "" {
		return nil, errors.New("buildNodeAgentJob: no CRI socket path given")
	}

	cfgJSON, err := agentconfig.MarshalTight(cfg)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal agent config: %w", err)
	}
	idsJSON, err := json.Marshal(opts.containerIDs)
	if err != nil {
		return nil, fmt.Errorf("unable to marshal container IDs: %w", err)
	}

	env := []core.EnvVar{
		{
			Name: agentconfig.EnvPrefixAgent + "POD_IP",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "status.podIP",
				},
			},
		},
		{
			Name: agentconfig.EnvPrefixAgent + "POD_UID",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.uid",
				},
			},
		},
		{
			Name: agentconfig.EnvPrefixAgent + "NAME",
			ValueFrom: &core.EnvVarSource{
				FieldRef: &core.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.name",
				},
			},
		},
		{
			Name:  agentconfig.EnvAgentConfig,
			Value: cfgJSON,
		},
		{
			Name:  agentconfig.EnvNodeAgentContainerIDs,
			Value: string(idsJSON),
		},
		{
			Name:  agentconfig.EnvNodeAgentPodIP,
			Value: opts.podIP,
		},
		{
			Name:  agentconfig.EnvNodeAgentCRISocket,
			Value: opts.criSocket,
		},
	}

	hostPathSocket := core.HostPathSocket
	volumes := []core.Volume{
		{
			Name: agentconfig.ExportsVolumeName,
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
		{
			Name: nodeAgentCRIVolumeName,
			VolumeSource: core.VolumeSource{
				HostPath: &core.HostPathVolumeSource{
					Path: opts.criSocket,
					Type: &hostPathSocket,
				},
			},
		},
	}
	mounts := []core.VolumeMount{
		{
			Name:      agentconfig.ExportsVolumeName,
			MountPath: agentconfig.ExportsMountPoint,
		},
		{
			Name:      nodeAgentCRIVolumeName,
			MountPath: opts.criSocket,
			ReadOnly:  true,
		},
	}

	backoffLimit := int32(0)
	ttl := nodeAgentTTLSecondsAfterFinished
	labels := map[string]string{
		nodeAgentAppLabel:       nodeAgentAppLabelValue,
		nodeAgentNameLabel:      cfg.AgentName,
		nodeAgentNamespaceLabel: cfg.Namespace,
		nodeAgentTargetPodLabel: opts.podName,
	}

	job := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      nodeAgentJobName(cfg.AgentName, cfg.Namespace, opts.podName),
			Namespace: opts.namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{
					Labels: labels,
				},
				Spec: core.PodSpec{
					NodeName:      opts.nodeName,
					HostPID:       true,
					RestartPolicy: core.RestartPolicyNever,
					Containers: []core.Container{
						{
							Name:  nodeAgentContainerName,
							Image: cfg.AgentImage,
							Args:  []string{"node-agent"},
							Env:   env,
							SecurityContext: &core.SecurityContext{
								// The node-agent needs root (uid 0) for the
								// capabilities below, but runs with the
								// traffic-agent's distinguishing group so its
								// own sockets in a target's network namespace
								// carry the skgid the owner-match rule keys
								// on (see agentnft.OwnerMatch).
								RunAsUser:  new(int64(0)),
								RunAsGroup: new(agentconfig.DefaultAgentGID),
								Capabilities: &core.Capabilities{
									Add: []core.Capability{"SYS_ADMIN", "SYS_PTRACE", "NET_ADMIN", "NET_RAW"},
								},
							},
							VolumeMounts: mounts,
						},
					},
					Volumes: volumes,
				},
			},
		},
	}
	return job, nil
}

// nodeAgentJobNamePrefix returns the deterministic, DNS-1123-compliant
// prefix shared by every Job created for agentName (truncated, like
// nodeAgentJobName, to leave room for the per-pod suffix). Every node-agent
// Job's name begins with this prefix, so it can be used to scope a watch (or
// other lookup) to the Jobs -- and, since a pod name is generated from its
// Job name, the pods -- created for that agent, without knowing the target
// pod in advance.
func nodeAgentJobNamePrefix(agentName string) string {
	const prefix = "tel-node-agent-"
	const suffixLen = 8                            // len(hex.EncodeToString(sha256 sum)[:8])
	maxBaseLen := 63 - len(prefix) - suffixLen - 1 // -1 for the separating hyphen
	if len(agentName) > maxBaseLen {
		agentName = agentName[:maxBaseLen]
	}
	agentName = trimTrailingDash(agentName)
	return prefix + agentName
}

// nodeAgentJobName returns a deterministic, DNS-1123-compliant Job name (at
// most 63 characters) derived from the agent name and the target's namespace
// and pod name. Determinism makes a retried request idempotent: it resolves
// to the Job already created for the same agent and target pod. The
// namespace participates in the hash so that two workloads with the same
// name and the same generated pod name, in different namespaces, never
// collide on a single Job name.
func nodeAgentJobName(agentName, namespace, podName string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + podName))
	suffix := hex.EncodeToString(sum[:])[:8]
	return nodeAgentJobNamePrefix(agentName) + "-" + suffix
}

// trimTrailingDash removes any trailing "-" characters left behind by
// truncating a DNS-1123 label, which must not end with a dash.
func trimTrailingDash(s string) string {
	i := len(s)
	for i > 0 && s[i-1] == '-' {
		i--
	}
	return s[:i]
}

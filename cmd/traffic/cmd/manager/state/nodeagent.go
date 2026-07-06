package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

const (
	// envNodeAgentContainerIDs mirrors the constant of the same name declared
	// in cmd/traffic/cmd/agent/nodeagent_linux.go. It is redeclared here
	// (rather than imported) because the traffic-manager must not import the
	// agent command package.
	envNodeAgentContainerIDs = "_TEL_NODE_AGENT_CONTAINER_IDS"

	// envNodeAgentCRISocket mirrors the constant of the same name declared in
	// cmd/traffic/cmd/agent/nodeagent_linux.go.
	envNodeAgentCRISocket = "_TEL_NODE_AGENT_CRI_SOCKET"

	// envNodeAgentPodIP mirrors the constant of the same name declared in
	// cmd/traffic/cmd/agent/nodeagent_linux.go.
	envNodeAgentPodIP = "_TEL_NODE_AGENT_POD_IP"

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

	// nodeAgentTargetPodLabel carries the name of the pod that the node-agent
	// Job targets.
	nodeAgentTargetPodLabel = "telepresence.io/targetPod"

	// nodeAgentTTLSecondsAfterFinished is how long a completed or failed
	// node-agent Job is kept around before being garbage collected.
	nodeAgentTTLSecondsAfterFinished = int32(300)
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

// nodeAgentNamespace returns the namespace that node-agent Jobs are created
// in: env.NodeAgentNamespace when set, otherwise the traffic-manager's own
// namespace.
func nodeAgentNamespace(env *managerutil.Env) string {
	if env.NodeAgentNamespace != "" {
		return env.NodeAgentNamespace
	}
	return env.ManagerNamespace
}

// reapNodeAgentJobs deletes the node-agent Job(s) created for agentName in
// the node-agent namespace. It is invoked as an intercept finalizer, so it
// runs both on explicit intercept removal and on client-session drop.
//
// NOTE: this deletes by the agentName label, so it reaps the Job for every
// node-agent intercept of that agent at once. That is correct while
// node-agent intercepts are 1:1 with the agent; supporting multiple
// concurrent node-agent intercepts sharing one Job is future work.
func reapNodeAgentJobs(ctx context.Context, agentName string) error {
	env := managerutil.GetEnv(ctx)
	ns := nodeAgentNamespace(env)
	sel := fmt.Sprintf("%s=%s,%s=%s", nodeAgentAppLabel, nodeAgentAppLabelValue, nodeAgentNameLabel, agentName)
	propagation := meta.DeletePropagationBackground
	err := k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(ns).DeleteCollection(ctx,
		meta.DeleteOptions{PropagationPolicy: &propagation},
		meta.ListOptions{LabelSelector: sel})
	if err != nil && !k8sErrors.IsNotFound(err) {
		return fmt.Errorf("unable to reap node-agent job(s) for agent %s.%s: %w", agentName, ns, err)
	}
	clog.Debugf(ctx, "reaped node-agent job(s) for agent %s.%s", agentName, ns)
	return nil
}

// ensureNodeAgent provisions a node-hosted traffic-agent (a manager-created
// Job that enters the target pod's namespaces) for the workload, using the
// agent config that the caller already generated via a dryRun ensureAgent
// call. It creates only the Job; it never touches the workload's pod
// template, so no sidecar is injected and no pod is restarted.
func (s *State) ensureNodeAgent(
	ctx context.Context,
	wl k8sapi.Workload,
	cfg *agentconfig.Sidecar,
) error {
	nodeName, containerIDs, podName, podIP, err := nodeAgentTarget(ctx, wl)
	if err != nil {
		return err
	}
	for _, cn := range cfg.Containers {
		if _, ok := containerIDs[cn.Name]; !ok {
			return errcat.User.Newf("unable to resolve a container ID for container %q in pod %s.%s", cn.Name, podName, wl.GetNamespace())
		}
	}

	env := managerutil.GetEnv(ctx)
	namespace := nodeAgentNamespace(env)

	job, err := buildNodeAgentJob(cfg, nodeAgentJobOpts{
		namespace:    namespace,
		nodeName:     nodeName,
		containerIDs: containerIDs,
		criSocket:    env.NodeAgentCRISocket,
		podName:      podName,
		podIP:        podIP,
	})
	if err != nil {
		return err
	}

	if _, err = k8sapi.GetK8sInterface(ctx).BatchV1().Jobs(namespace).Create(ctx, job, meta.CreateOptions{}); err != nil {
		if !k8sErrors.IsAlreadyExists(err) {
			return fmt.Errorf("unable to create node-agent job %s.%s: %w", job.Name, namespace, err)
		}
		// A Job with this deterministic name already exists for this agent
		// and target pod; treat a re-request as idempotent.
		clog.Debugf(ctx, "node-agent job %s.%s already exists", job.Name, namespace)
	}
	return nil
}

// nodeAgentTarget selects a single Running & Ready pod of wl (a node-agent
// targets one pod, unlike the sidecar which is injected into every replica)
// and returns the node it is scheduled on, a map of container name to CRI
// container ID (as reported by the kubelet, including the runtime scheme
// prefix, e.g. "containerd://abc123"; pkg/cri strips that prefix), the pod's
// name, and the pod's IP (which the node-agent needs as the PodIP of the
// netfilter ruleset it programs into the target's network namespace).
func nodeAgentTarget(ctx context.Context, wl k8sapi.Workload) (nodeName string, containerIDs map[string]string, podName, podIP string, err error) {
	selector, err := wl.Selector()
	if err != nil {
		return "", nil, "", "", err
	}
	pods, err := k8sapi.GetK8sInterface(ctx).CoreV1().Pods(wl.GetNamespace()).List(ctx, meta.ListOptions{
		LabelSelector: selector.String(),
	})
	if err != nil {
		return "", nil, "", "", fmt.Errorf("unable to list pods for %s: %w", wl, err)
	}

	var target *core.Pod
	for i := range pods.Items {
		if pod := &pods.Items[i]; podRunningAndReady(pod) {
			target = pod
			break
		}
	}
	if target == nil {
		return "", nil, "", "", errcat.User.Newf("%s has no running and ready pod available to host a node-agent", wl)
	}
	if target.Status.PodIP == "" {
		return "", nil, "", "", fmt.Errorf("pod %s.%s has no PodIP despite being running and ready", target.Name, target.Namespace)
	}

	ids := make(map[string]string, len(target.Status.ContainerStatuses))
	for _, cs := range target.Status.ContainerStatuses {
		if cs.ContainerID != "" {
			ids[cs.Name] = cs.ContainerID
		}
	}
	return target.Spec.NodeName, ids, target.Name, target.Status.PodIP, nil
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
	// namespace is where the Job is created. This is the traffic-manager's
	// node-agent namespace, not necessarily the target pod's namespace.
	namespace string

	// nodeName is the node that the target pod is scheduled on. The Job's
	// pod is pinned to it via Spec.NodeName.
	nodeName string

	// containerIDs maps configured agent container name to CRI container ID
	// (including the runtime scheme prefix) of the target pod.
	containerIDs map[string]string

	// criSocket is the container-runtime socket path to mount into the
	// node-agent container. When empty, no hostPath volume is added and the
	// node-agent falls back to cri.DetectSocket.
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
			Name:  envNodeAgentContainerIDs,
			Value: string(idsJSON),
		},
		{
			Name:  envNodeAgentPodIP,
			Value: opts.podIP,
		},
	}
	if opts.criSocket != "" {
		env = append(env, core.EnvVar{
			Name:  envNodeAgentCRISocket,
			Value: opts.criSocket,
		})
	}

	volumes := []core.Volume{
		{
			Name: agentconfig.ExportsVolumeName,
			VolumeSource: core.VolumeSource{
				EmptyDir: &core.EmptyDirVolumeSource{},
			},
		},
	}
	mounts := []core.VolumeMount{
		{
			Name:      agentconfig.ExportsVolumeName,
			MountPath: agentconfig.ExportsMountPoint,
		},
	}
	if opts.criSocket != "" {
		hostPathSocket := core.HostPathSocket
		volumes = append(volumes, core.Volume{
			Name: nodeAgentCRIVolumeName,
			VolumeSource: core.VolumeSource{
				HostPath: &core.HostPathVolumeSource{
					Path: opts.criSocket,
					Type: &hostPathSocket,
				},
			},
		})
		mounts = append(mounts, core.VolumeMount{
			Name:      nodeAgentCRIVolumeName,
			MountPath: opts.criSocket,
			ReadOnly:  true,
		})
	}

	backoffLimit := int32(0)
	ttl := nodeAgentTTLSecondsAfterFinished
	labels := map[string]string{
		nodeAgentAppLabel:       nodeAgentAppLabelValue,
		nodeAgentNameLabel:      cfg.AgentName,
		nodeAgentTargetPodLabel: opts.podName,
	}

	job := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      nodeAgentJobName(cfg.AgentName, opts.podName),
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

// nodeAgentJobName returns a deterministic, DNS-1123-compliant Job name (at
// most 63 characters) derived from the agent name and the target pod name.
// Determinism makes a retried request idempotent: it resolves to the Job
// already created for the same agent and target pod.
func nodeAgentJobName(agentName, podName string) string {
	sum := sha256.Sum256([]byte(podName))
	suffix := hex.EncodeToString(sum[:])[:8]

	const prefix = "tel-node-agent-"
	maxBaseLen := 63 - len(prefix) - len(suffix) - 1 // -1 for the separating hyphen
	if len(agentName) > maxBaseLen {
		agentName = agentName[:maxBaseLen]
	}
	agentName = trimTrailingDash(agentName)
	return prefix + agentName + "-" + suffix
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

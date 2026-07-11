package state

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apps "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/v2/pkg/tunnel"
)

// TestNodeAgentGateErr_Disabled verifies that requesting node-agent mode
// while the traffic-manager has not enabled it produces a User-categorized
// error, which is the gate PrepareIntercept applies before ever attempting
// to provision a node-hosted agent.
func TestNodeAgentGateErr_Disabled(t *testing.T) {
	t.Parallel()

	env := &managerutil.Env{NodeAgentEnabled: false}
	err := nodeAgentGateErr(env)
	require.Error(t, err)
	assert.Equal(t, errcat.User, errcat.GetCategory(err))
	assert.Contains(t, err.Error(), "node-agent mode is not enabled")
}

// TestNodeAgentGateErr_Enabled verifies that the gate lets requests through
// once node-agent mode is enabled on the traffic-manager.
func TestNodeAgentGateErr_Enabled(t *testing.T) {
	t.Parallel()

	env := &managerutil.Env{NodeAgentEnabled: true}
	assert.NoError(t, nodeAgentGateErr(env))
}

// installDeleteCollectionReactor installs a reactor that emulates a real API
// server's "delete-collection" verb for Jobs: list with the given selector,
// then delete each match. The fake clientset's generic object-tracker
// reactor does not implement that verb (it silently no-ops), which is why
// reapNodeAgentJobs -- and any test that exercises it, directly or via
// ReleaseAgent/the intercept-removal finalizer -- needs this installed on
// its clientset. It operates on the tracker directly (rather than calling
// back through ci.BatchV1()...), because that call path re-enters ci's
// non-reentrant lock, which is already held by the Invokes call that
// dispatches to this reactor.
func installDeleteCollectionReactor(ci *fake.Clientset) {
	ci.PrependReactor("delete-collection", "jobs", func(action k8stesting.Action) (bool, runtime.Object, error) {
		dc, ok := action.(k8stesting.DeleteCollectionActionImpl)
		if !ok {
			return false, nil, nil
		}
		gvr := dc.GetResource()
		tracker := ci.Tracker()
		obj, err := tracker.List(gvr, batchv1.SchemeGroupVersion.WithKind("Job"), dc.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		jobList, ok := obj.(*batchv1.JobList)
		if !ok {
			return true, nil, fmt.Errorf("unexpected list type %T", obj)
		}
		sel := dc.GetListRestrictions().Labels
		for _, job := range jobList.Items {
			if sel == nil || sel.Matches(labels.Set(job.Labels)) {
				if err := tracker.Delete(gvr, dc.GetNamespace(), job.Name); err != nil {
					return true, nil, err
				}
			}
		}
		return true, nil, nil
	})
}

// TestReapNodeAgentJobs verifies that reaping deletes only the Job(s)
// matching the app + agentName + workloadNamespace labels in the
// traffic-manager's namespace, leaving unrelated Jobs (different agent,
// different workload namespace, or missing the app label) untouched.
func TestReapNodeAgentJobs(t *testing.T) {
	t.Parallel()

	const ns = "ambassador"
	matching := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      "tel-node-agent-match",
			Namespace: ns,
			Labels: map[string]string{
				nodeAgentAppLabel:       nodeAgentAppLabelValue,
				nodeAgentNameLabel:      "test-agent",
				nodeAgentNamespaceLabel: "ns1",
			},
		},
	}
	other := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      "tel-node-agent-other",
			Namespace: ns,
			Labels: map[string]string{
				nodeAgentAppLabel:       nodeAgentAppLabelValue,
				nodeAgentNameLabel:      "other-agent",
				nodeAgentNamespaceLabel: "ns1",
			},
		},
	}
	sameNameOtherNamespace := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      "tel-node-agent-ns2",
			Namespace: ns,
			Labels: map[string]string{
				nodeAgentAppLabel:       nodeAgentAppLabelValue,
				nodeAgentNameLabel:      "test-agent",
				nodeAgentNamespaceLabel: "ns2",
			},
		},
	}
	unrelated := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      "some-other-job",
			Namespace: ns,
		},
	}

	ci := fake.NewSimpleClientset(matching, other, sameNameOtherNamespace, unrelated)
	installDeleteCollectionReactor(ci)

	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: ns})

	require.NoError(t, reapNodeAgentJobs(ctx, "test-agent", "ns1"))

	jobs, err := ci.BatchV1().Jobs(ns).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	names := make([]string, len(jobs.Items))
	for i, j := range jobs.Items {
		names[i] = j.Name
	}
	assert.NotContains(t, names, matching.Name)
	assert.Contains(t, names, other.Name)
	assert.Contains(t, names, sameNameOtherNamespace.Name, "reaping agent in ns1 must not delete the same agent's Job in ns2")
	assert.Contains(t, names, unrelated.Name)
}

// TestReapAllNodeAgentJobs verifies that an uninstall-time reap deletes every
// node-agent Job in the traffic-manager's namespace regardless of which
// agent or workload namespace it was created for, while a Job that lacks
// the app label (i.e. not a node-agent Job at all) survives.
func TestReapAllNodeAgentJobs(t *testing.T) {
	t.Parallel()

	const ns = "ambassador"
	first := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      "tel-node-agent-first",
			Namespace: ns,
			Labels: map[string]string{
				nodeAgentAppLabel:       nodeAgentAppLabelValue,
				nodeAgentNameLabel:      "test-agent",
				nodeAgentNamespaceLabel: "ns1",
			},
		},
	}
	second := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      "tel-node-agent-second",
			Namespace: ns,
			Labels: map[string]string{
				nodeAgentAppLabel:       nodeAgentAppLabelValue,
				nodeAgentNameLabel:      "other-agent",
				nodeAgentNamespaceLabel: "ns2",
			},
		},
	}
	unrelated := &batchv1.Job{
		ObjectMeta: meta.ObjectMeta{
			Name:      "some-other-job",
			Namespace: ns,
		},
	}

	ci := fake.NewSimpleClientset(first, second, unrelated)
	installDeleteCollectionReactor(ci)

	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: ns})

	require.NoError(t, ReapAllNodeAgentJobs(ctx))

	jobs, err := ci.BatchV1().Jobs(ns).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	names := make([]string, len(jobs.Items))
	for i, j := range jobs.Items {
		names[i] = j.Name
	}
	assert.NotContains(t, names, first.Name)
	assert.NotContains(t, names, second.Name)
	assert.Contains(t, names, unrelated.Name)
}

// TestReapAllNodeAgentJobs_None verifies that reaping succeeds when the
// traffic-manager's namespace has no node-agent Jobs at all.
func TestReapAllNodeAgentJobs_None(t *testing.T) {
	t.Parallel()

	const ns = "ambassador"
	ci := fake.NewSimpleClientset()
	installDeleteCollectionReactor(ci)

	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: ns})

	require.NoError(t, ReapAllNodeAgentJobs(ctx))
}

// TestCheckNodeAgentTarget verifies that a target pod which cannot host a
// node-agent -- one on the host network, or one that already carries an
// injected traffic-agent sidecar -- is rejected with a User error, while an
// ordinary pod is accepted.
func TestCheckNodeAgentTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pod     *core.Pod
		wantErr string
	}{
		{
			name: "ordinary pod is accepted",
			pod: &core.Pod{
				ObjectMeta: meta.ObjectMeta{Name: "app-1", Namespace: "ns"},
				Spec:       core.PodSpec{Containers: []core.Container{{Name: "app"}}},
			},
		},
		{
			name: "host network is rejected",
			pod: &core.Pod{
				ObjectMeta: meta.ObjectMeta{Name: "app-1", Namespace: "ns"},
				Spec:       core.PodSpec{HostNetwork: true, Containers: []core.Container{{Name: "app"}}},
			},
			wantErr: "host networking",
		},
		{
			name: "injected sidecar is rejected",
			pod: &core.Pod{
				ObjectMeta: meta.ObjectMeta{Name: "app-1", Namespace: "ns"},
				Spec: core.PodSpec{Containers: []core.Container{
					{Name: "app"},
					{Name: agentconfig.ContainerName},
				}},
			},
			wantErr: "already has an injected traffic-agent",
		},
		{
			name: "user namespace is rejected",
			pod: &core.Pod{
				ObjectMeta: meta.ObjectMeta{Name: "app-1", Namespace: "ns"},
				Spec:       core.PodSpec{HostUsers: new(false), Containers: []core.Container{{Name: "app"}}},
			},
			wantErr: "user namespace",
		},
		{
			name: "explicit host users is accepted",
			pod: &core.Pod{
				ObjectMeta: meta.ObjectMeta{Name: "app-1", Namespace: "ns"},
				Spec:       core.PodSpec{HostUsers: new(true), Containers: []core.Container{{Name: "app"}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkNodeAgentTarget(tt.pod)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, errcat.User, errcat.GetCategory(err))
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestReconcileNodeAgentJobs verifies the orphan sweep: a Job whose agent has a
// live node-agent intercept in the same workload namespace is kept; a Job
// younger than the grace period is kept even with no intercept; a Job for the
// same agent name but a different workload namespace than any live intercept
// is reaped; and only an old Job with no matching intercept is reaped.
func TestReconcileNodeAgentJobs(t *testing.T) {
	t.Parallel()

	const ns = "ambassador"
	naJob := func(name, agentName, workloadNs string, age time.Duration) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: meta.ObjectMeta{
				Name:              name,
				Namespace:         ns,
				CreationTimestamp: meta.NewTime(time.Now().Add(-age)),
				Labels: map[string]string{
					nodeAgentAppLabel:       nodeAgentAppLabelValue,
					nodeAgentNameLabel:      agentName,
					nodeAgentNamespaceLabel: workloadNs,
				},
			},
		}
	}

	wanted := naJob("tel-node-agent-wanted", "live-agent", "ns1", 10*time.Minute)
	youngOrphan := naJob("tel-node-agent-young", "gone-agent", "ns1", nodeAgentOrphanGracePeriod/2)
	oldOrphan := naJob("tel-node-agent-old", "gone-agent", "ns1", 10*time.Minute)
	otherNamespace := naJob("tel-node-agent-ns2", "live-agent", "ns2", 10*time.Minute)

	ci := fake.NewSimpleClientset(wanted, youngOrphan, oldOrphan, otherNamespace)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: ns})

	s := &State{
		backgroundCtx: ctx,
		intercepts:    cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		clients:       xsync.NewMap[tunnel.SessionID, *ClientSession](),
		leases:        xsync.NewMap[leaseKey, struct{}](),
	}
	s.intercepts.Store("s:live-agent", &Intercept{InterceptInfo: &rpc.InterceptInfo{
		Disposition: rpc.InterceptDispositionType_ACTIVE,
		Spec:        &rpc.InterceptSpec{Agent: "live-agent", Namespace: "ns1", NodeAgent: true},
	}})

	require.NoError(t, s.reconcileNodeAgentJobs(ctx))

	jobs, err := ci.BatchV1().Jobs(ns).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	names := make([]string, len(jobs.Items))
	for i, j := range jobs.Items {
		names[i] = j.Name
	}
	assert.Contains(t, names, wanted.Name, "Job with a live intercept must be kept")
	assert.Contains(t, names, youngOrphan.Name, "Job younger than the grace period must be kept")
	assert.NotContains(t, names, oldOrphan.Name, "old orphaned Job must be reaped")
	assert.NotContains(t, names, otherNamespace.Name,
		"a live intercept for agent X in ns1 must not protect a Job labeled agent X in ns2")
}

// newNodeAgentTestState returns a State with the maps ensureNodeAgent's
// nodeAgentWanted check needs (intercepts, leases, clients), all empty, so
// tests that call ensureNodeAgent directly don't hit a nil map.
func newNodeAgentTestState() *State {
	return &State{
		intercepts: cache.NewMap[string, *Intercept](interceptEqual, time.Millisecond),
		leases:     xsync.NewMap[leaseKey, struct{}](),
		clients:    xsync.NewMap[tunnel.SessionID, *ClientSession](),
	}
}

func testSidecar() *agentconfig.Sidecar {
	return &agentconfig.Sidecar{
		AgentName:  "test-agent",
		AgentImage: "ghcr.io/telepresenceio/tel2:2.99.0",
		Namespace:  "test-namespace",
		Containers: []*agentconfig.Container{
			{Name: "app"},
		},
	}
}

func testOpts() nodeAgentJobOpts {
	return nodeAgentJobOpts{
		namespace:    "ambassador",
		nodeName:     "node-1",
		containerIDs: map[string]string{"app": "containerd://abc123"},
		criSocket:    "/run/containerd/containerd.sock",
		podName:      "test-agent-abc123",
		podIP:        "10.42.0.5",
	}
}

// TestBuildNodeAgentJob_PodSpec verifies the pod-security posture that a
// node-agent Job needs in order to enter the target pod's namespaces:
// scheduling onto the target's node, sharing the host PID namespace, and
// never restarting in place (a fresh Job is created per request instead).
func TestBuildNodeAgentJob_PodSpec(t *testing.T) {
	t.Parallel()

	job, err := buildNodeAgentJob(testSidecar(), testOpts())
	require.NoError(t, err)

	podSpec := job.Spec.Template.Spec
	assert.Equal(t, "node-1", podSpec.NodeName)
	assert.True(t, podSpec.HostPID)
	assert.Equal(t, core.RestartPolicyNever, podSpec.RestartPolicy)
	assert.Equal(t, "ambassador", job.Namespace)
	require.Len(t, podSpec.Containers, 1)
}

// TestBuildNodeAgentJob_ContainerImageAndCommand verifies that the container
// runs the same image as the sidecar traffic-agent and selects the
// node-agent subcommand the same way agent-init selects its own subcommand:
// via Args, relying on the image's ENTRYPOINT ["traffic"].
func TestBuildNodeAgentJob_ContainerImageAndCommand(t *testing.T) {
	t.Parallel()

	job, err := buildNodeAgentJob(testSidecar(), testOpts())
	require.NoError(t, err)

	cn := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "ghcr.io/telepresenceio/tel2:2.99.0", cn.Image)
	assert.Empty(t, cn.Command)
	assert.Equal(t, []string{"node-agent"}, cn.Args)
}

func findEnv(vars []core.EnvVar, name string) (core.EnvVar, bool) {
	for _, v := range vars {
		if v.Name == name {
			return v, true
		}
	}
	return core.EnvVar{}, false
}

// TestBuildNodeAgentJob_Env verifies the environment the node-agent needs:
// its own AGENT_CONFIG (set directly, since there's no pod annotation to
// source it from the way the sidecar does), the three downward-API
// "_TEL_AGENT_" vars copied from the sidecar construction, and the two
// node-agent-specific vars carrying the target's container IDs and CRI
// socket.
func TestBuildNodeAgentJob_Env(t *testing.T) {
	t.Parallel()

	cfg := testSidecar()
	opts := testOpts()
	opts.criSocket = "/run/containerd/containerd.sock"
	job, err := buildNodeAgentJob(cfg, opts)
	require.NoError(t, err)

	env := job.Spec.Template.Spec.Containers[0].Env

	acEnv, ok := findEnv(env, agentconfig.EnvAgentConfig)
	require.True(t, ok, "AGENT_CONFIG env var not found")
	assert.NotEmpty(t, acEnv.Value)
	assert.Nil(t, acEnv.ValueFrom)

	podIP, ok := findEnv(env, agentconfig.EnvPrefixAgent+"POD_IP")
	require.True(t, ok)
	require.NotNil(t, podIP.ValueFrom)
	require.NotNil(t, podIP.ValueFrom.FieldRef)
	assert.Equal(t, "status.podIP", podIP.ValueFrom.FieldRef.FieldPath)

	podUID, ok := findEnv(env, agentconfig.EnvPrefixAgent+"POD_UID")
	require.True(t, ok)
	require.NotNil(t, podUID.ValueFrom)
	require.NotNil(t, podUID.ValueFrom.FieldRef)
	assert.Equal(t, "metadata.uid", podUID.ValueFrom.FieldRef.FieldPath)

	name, ok := findEnv(env, agentconfig.EnvPrefixAgent+"NAME")
	require.True(t, ok)
	require.NotNil(t, name.ValueFrom)
	require.NotNil(t, name.ValueFrom.FieldRef)
	assert.Equal(t, "metadata.name", name.ValueFrom.FieldRef.FieldPath)

	idsEnv, ok := findEnv(env, agentconfig.EnvNodeAgentContainerIDs)
	require.True(t, ok)
	var decoded map[string]string
	require.NoError(t, json.Unmarshal([]byte(idsEnv.Value), &decoded))
	assert.Equal(t, opts.containerIDs, decoded)

	podIPEnv, ok := findEnv(env, agentconfig.EnvNodeAgentPodIP)
	require.True(t, ok, "%s env var not found", agentconfig.EnvNodeAgentPodIP)
	assert.Equal(t, opts.podIP, podIPEnv.Value)
	assert.Nil(t, podIPEnv.ValueFrom)

	criEnv, ok := findEnv(env, agentconfig.EnvNodeAgentCRISocket)
	require.True(t, ok)
	assert.Equal(t, "/run/containerd/containerd.sock", criEnv.Value)
}

// TestBuildNodeAgentJob_NoCRISocket verifies the auto-detect posture of an
// empty criSocket: the node's /run is mounted read-only at
// agentconfig.NodeAgentHostRunDir via a hostPath volume of type Directory,
// and no CRI socket env is set, leaving the agent to probe the well-known
// sockets beneath the mount.
func TestBuildNodeAgentJob_NoCRISocket(t *testing.T) {
	t.Parallel()

	opts := testOpts()
	opts.criSocket = ""
	job, err := buildNodeAgentJob(testSidecar(), opts)
	require.NoError(t, err)

	podSpec := job.Spec.Template.Spec
	var vol *core.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == nodeAgentCRIVolumeName {
			vol = &podSpec.Volumes[i]
			break
		}
	}
	require.NotNil(t, vol, "CRI volume not found")
	require.NotNil(t, vol.HostPath)
	assert.Equal(t, "/run", vol.HostPath.Path)
	require.NotNil(t, vol.HostPath.Type)
	assert.Equal(t, core.HostPathDirectory, *vol.HostPath.Type)

	var mount *core.VolumeMount
	for i := range podSpec.Containers[0].VolumeMounts {
		if podSpec.Containers[0].VolumeMounts[i].Name == nodeAgentCRIVolumeName {
			mount = &podSpec.Containers[0].VolumeMounts[i]
			break
		}
	}
	require.NotNil(t, mount, "CRI mount not found")
	assert.Equal(t, agentconfig.NodeAgentHostRunDir, mount.MountPath)
	assert.True(t, mount.ReadOnly)

	_, ok := findEnv(podSpec.Containers[0].Env, agentconfig.EnvNodeAgentCRISocket)
	assert.False(t, ok, "no CRI socket env should be set when auto-detecting")
}

// TestBuildNodeAgentJob_Capabilities verifies the exact set of Linux
// capabilities the node-agent's documented posture needs: /proc access and
// namespace entry (SYS_ADMIN, SYS_PTRACE) and netfilter programming
// (NET_ADMIN, NET_RAW). Privileged is deliberately not set.
func TestBuildNodeAgentJob_Capabilities(t *testing.T) {
	t.Parallel()

	job, err := buildNodeAgentJob(testSidecar(), testOpts())
	require.NoError(t, err)

	cn := job.Spec.Template.Spec.Containers[0]
	require.NotNil(t, cn.SecurityContext)
	if cn.SecurityContext.Privileged != nil {
		assert.False(t, *cn.SecurityContext.Privileged)
	}
	require.NotNil(t, cn.SecurityContext.Capabilities)
	assert.ElementsMatch(t, []core.Capability{"SYS_ADMIN", "SYS_PTRACE", "NET_ADMIN", "NET_RAW"}, cn.SecurityContext.Capabilities.Add)
}

// TestBuildNodeAgentJob_RunAsUserGroup verifies that the node-agent container
// runs as root (RunAsUser 0, required for the capabilities above) but with
// the traffic-agent's distinguishing primary group, so its own sockets in the
// target's network namespace carry the skgid the owner-match rule
// (agentnft.OwnerMatch) keys on.
func TestBuildNodeAgentJob_RunAsUserGroup(t *testing.T) {
	t.Parallel()

	job, err := buildNodeAgentJob(testSidecar(), testOpts())
	require.NoError(t, err)

	cn := job.Spec.Template.Spec.Containers[0]
	require.NotNil(t, cn.SecurityContext)
	require.NotNil(t, cn.SecurityContext.RunAsUser)
	assert.Equal(t, int64(0), *cn.SecurityContext.RunAsUser)
	require.NotNil(t, cn.SecurityContext.RunAsGroup)
	assert.Equal(t, agentconfig.DefaultAgentGID, *cn.SecurityContext.RunAsGroup)
}

// TestBuildNodeAgentJob_ExportsVolume verifies that the node-agent gets an
// emptyDir mounted at agentconfig.ExportsMountPoint to populate the symlink
// tree it serves remote mounts from, matching the sidecar's own exports
// volume.
func TestBuildNodeAgentJob_ExportsVolume(t *testing.T) {
	t.Parallel()

	job, err := buildNodeAgentJob(testSidecar(), testOpts())
	require.NoError(t, err)

	podSpec := job.Spec.Template.Spec
	var vol *core.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == agentconfig.ExportsVolumeName {
			vol = &podSpec.Volumes[i]
			break
		}
	}
	require.NotNil(t, vol, "exports volume not found")
	require.NotNil(t, vol.EmptyDir)

	var mount *core.VolumeMount
	for i := range podSpec.Containers[0].VolumeMounts {
		if podSpec.Containers[0].VolumeMounts[i].Name == agentconfig.ExportsVolumeName {
			mount = &podSpec.Containers[0].VolumeMounts[i]
			break
		}
	}
	require.NotNil(t, mount, "exports mount not found")
	assert.Equal(t, agentconfig.ExportsMountPoint, mount.MountPath)
}

// TestBuildNodeAgentJob_CRIVolume verifies that, when a CRI socket path is
// configured, it is mounted read-only into the node-agent container via a
// hostPath volume of type Socket.
func TestBuildNodeAgentJob_CRIVolume(t *testing.T) {
	t.Parallel()

	opts := testOpts()
	opts.criSocket = "/run/containerd/containerd.sock"
	job, err := buildNodeAgentJob(testSidecar(), opts)
	require.NoError(t, err)

	podSpec := job.Spec.Template.Spec
	var vol *core.Volume
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == nodeAgentCRIVolumeName {
			vol = &podSpec.Volumes[i]
			break
		}
	}
	require.NotNil(t, vol, "CRI socket volume not found")
	require.NotNil(t, vol.HostPath)
	assert.Equal(t, opts.criSocket, vol.HostPath.Path)
	require.NotNil(t, vol.HostPath.Type)
	assert.Equal(t, core.HostPathSocket, *vol.HostPath.Type)

	var mount *core.VolumeMount
	for i := range podSpec.Containers[0].VolumeMounts {
		if podSpec.Containers[0].VolumeMounts[i].Name == nodeAgentCRIVolumeName {
			mount = &podSpec.Containers[0].VolumeMounts[i]
			break
		}
	}
	require.NotNil(t, mount, "CRI socket mount not found")
	assert.Equal(t, opts.criSocket, mount.MountPath)
	assert.True(t, mount.ReadOnly)
}

// TestBuildNodeAgentJob_Labels verifies the labels a later change (Helm
// enablement / reaping) needs in order to find node-agent Jobs and tie them
// back to the agent and target pod they were created for.
func TestBuildNodeAgentJob_Labels(t *testing.T) {
	t.Parallel()

	job, err := buildNodeAgentJob(testSidecar(), testOpts())
	require.NoError(t, err)

	assert.Equal(t, "traffic-node-agent", job.Labels[nodeAgentAppLabel])
	assert.Equal(t, "test-agent", job.Labels[nodeAgentNameLabel])
	assert.Equal(t, "test-namespace", job.Labels[nodeAgentNamespaceLabel])
	assert.Equal(t, "test-agent-abc123", job.Labels[nodeAgentTargetPodLabel])
}

// TestBuildNodeAgentJob_JobSpec verifies that a failed Job entry is not
// retried and that a finished Job self-cleans after a while.
func TestBuildNodeAgentJob_JobSpec(t *testing.T) {
	t.Parallel()

	job, err := buildNodeAgentJob(testSidecar(), testOpts())
	require.NoError(t, err)

	require.NotNil(t, job.Spec.BackoffLimit)
	assert.Equal(t, int32(0), *job.Spec.BackoffLimit)
	require.NotNil(t, job.Spec.TTLSecondsAfterFinished)
	assert.Positive(t, *job.Spec.TTLSecondsAfterFinished)
}

// TestBuildNodeAgentJob_NameLength verifies that the generated Job name
// stays within the 63-character DNS-1123 limit even for a long agent name.
func TestBuildNodeAgentJob_NameLength(t *testing.T) {
	t.Parallel()

	cfg := testSidecar()
	cfg.AgentName = "a-very-long-agent-name-that-is-longer-than-sixty-three-characters-in-total"
	job, err := buildNodeAgentJob(cfg, testOpts())
	require.NoError(t, err)
	assert.LessOrEqual(t, len(job.Name), 63)
}

// TestBuildNodeAgentJob_EmptyNodeName verifies that an impossible input
// (no node to schedule onto) is rejected rather than producing a Job that
// the scheduler would leave pending forever.
func TestBuildNodeAgentJob_EmptyNodeName(t *testing.T) {
	t.Parallel()

	opts := testOpts()
	opts.nodeName = ""
	_, err := buildNodeAgentJob(testSidecar(), opts)
	require.Error(t, err)
}

// TestBuildNodeAgentJob_EmptyContainerIDs verifies that an impossible input
// (no resolved container to attach to) is rejected.
func TestBuildNodeAgentJob_EmptyContainerIDs(t *testing.T) {
	t.Parallel()

	opts := testOpts()
	opts.containerIDs = nil
	_, err := buildNodeAgentJob(testSidecar(), opts)
	require.Error(t, err)
}

// TestBuildNodeAgentJob_EmptyPodIP verifies that an impossible input (no
// target pod IP resolved) is rejected, since the node-agent needs it as the
// PodIP of the netfilter ruleset it programs.
func TestBuildNodeAgentJob_EmptyPodIP(t *testing.T) {
	t.Parallel()

	opts := testOpts()
	opts.podIP = ""
	_, err := buildNodeAgentJob(testSidecar(), opts)
	require.Error(t, err)
}

// TestNodeAgentJobNamePrefix_MatchesJobName verifies that
// nodeAgentJobNamePrefix produces exactly the base that nodeAgentJobName
// appends its hash suffix to, both for an ordinary agent name and for one
// long enough to require truncation. A caller (such as the failure-event
// watch scoped to a Job's Jobs/pods) relies on this to recognize every Job
// and pod created for an agent without knowing the target pod name.
func TestNodeAgentJobNamePrefix_MatchesJobName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentName string
		namespace string
		podName   string
	}{
		{name: "short agent name", agentName: "test-agent", namespace: "ns1", podName: "test-agent-abc123"},
		{
			name:      "long agent name is truncated",
			agentName: "a-very-long-agent-name-that-is-longer-than-sixty-three-characters-in-total",
			namespace: "ns1",
			podName:   "some-pod",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jobName := nodeAgentJobName(tt.agentName, tt.namespace, tt.podName)
			prefix := nodeAgentJobNamePrefix(tt.agentName)
			suffix := jobName[len(jobName)-8:]
			assert.Equal(t, prefix+"-"+suffix, jobName)
			assert.True(t, strings.HasPrefix(jobName, prefix+"-"))
		})
	}
}

// TestNodeAgentJobStale verifies each condition that marks an existing
// node-agent Job as stale (so ensureNodeAgent replaces it instead of reusing
// it), that a difference confined to the marshaled agent config -- which
// does not identify the target -- does not, and that the image-change reason
// is classified separately (nodeAgentStaleImage) from every other staleness
// reason (nodeAgentStaleFatal), since only the image-change reason may be
// deferred while a live claim depends on the Job.
func TestNodeAgentJobStale(t *testing.T) {
	t.Parallel()

	desired, err := buildNodeAgentJob(testSidecar(), testOpts())
	require.NoError(t, err)

	t.Run("identical job is not stale", func(t *testing.T) {
		t.Parallel()
		existing := desired.DeepCopy()
		staleness, reason := nodeAgentJobStale(existing, desired)
		assert.Equal(t, nodeAgentFresh, staleness, reason)
	})

	t.Run("agent config differs is not stale", func(t *testing.T) {
		t.Parallel()
		cfg := testSidecar()
		cfg.ManagerPort = 9999 // changes the marshaled AGENT_CONFIG, nothing else
		existing, err := buildNodeAgentJob(cfg, testOpts())
		require.NoError(t, err)
		staleness, reason := nodeAgentJobStale(existing, desired)
		assert.Equal(t, nodeAgentFresh, staleness, reason)
	})

	t.Run("terminating job is fatally stale", func(t *testing.T) {
		t.Parallel()
		existing := desired.DeepCopy()
		now := meta.Now()
		existing.DeletionTimestamp = &now
		staleness, reason := nodeAgentJobStale(existing, desired)
		assert.Equal(t, nodeAgentStaleFatal, staleness)
		assert.Contains(t, reason, "terminating")
	})

	t.Run("failed job is fatally stale", func(t *testing.T) {
		t.Parallel()
		existing := desired.DeepCopy()
		existing.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailed, Status: core.ConditionTrue},
		}
		staleness, reason := nodeAgentJobStale(existing, desired)
		assert.Equal(t, nodeAgentStaleFatal, staleness)
		assert.Contains(t, reason, "failed")
	})

	t.Run("image differs is stale but not fatal", func(t *testing.T) {
		t.Parallel()
		cfg := testSidecar()
		cfg.AgentImage = "ghcr.io/telepresenceio/tel2:2.100.0"
		existing, err := buildNodeAgentJob(cfg, testOpts())
		require.NoError(t, err)
		staleness, reason := nodeAgentJobStale(existing, desired)
		assert.Equal(t, nodeAgentStaleImage, staleness)
		assert.Contains(t, reason, "image")
	})

	t.Run("container IDs differ is fatally stale", func(t *testing.T) {
		t.Parallel()
		opts := testOpts()
		opts.containerIDs = map[string]string{"app": "containerd://different"}
		existing, err := buildNodeAgentJob(testSidecar(), opts)
		require.NoError(t, err)
		staleness, reason := nodeAgentJobStale(existing, desired)
		assert.Equal(t, nodeAgentStaleFatal, staleness)
		assert.Contains(t, reason, agentconfig.EnvNodeAgentContainerIDs)
	})

	t.Run("pod IP differs is fatally stale", func(t *testing.T) {
		t.Parallel()
		opts := testOpts()
		opts.podIP = "10.42.0.99"
		existing, err := buildNodeAgentJob(testSidecar(), opts)
		require.NoError(t, err)
		staleness, reason := nodeAgentJobStale(existing, desired)
		assert.Equal(t, nodeAgentStaleFatal, staleness)
		assert.Contains(t, reason, agentconfig.EnvNodeAgentPodIP)
	})

	t.Run("container IDs differ takes priority over image differing", func(t *testing.T) {
		t.Parallel()
		cfg := testSidecar()
		cfg.AgentImage = "ghcr.io/telepresenceio/tel2:2.100.0"
		opts := testOpts()
		opts.containerIDs = map[string]string{"app": "containerd://different"}
		existing, err := buildNodeAgentJob(cfg, opts)
		require.NoError(t, err)
		staleness, reason := nodeAgentJobStale(existing, desired)
		assert.Equal(t, nodeAgentStaleFatal, staleness,
			"a fatal reason must be reported even when the image also differs")
		assert.Contains(t, reason, agentconfig.EnvNodeAgentContainerIDs)
	})
}

// nodeAgentTestPod returns a Running & Ready pod that nodeAgentTargets will
// select: it carries the labels nodeAgentTestWorkload's Deployment selects
// on, a resolved container ID for its sole "app" container, and a PodIP.
func nodeAgentTestPod(ns, podName string) *core.Pod {
	return &core.Pod{
		ObjectMeta: meta.ObjectMeta{
			Name:      podName,
			Namespace: ns,
			Labels:    map[string]string{"app": "test-agent"},
		},
		Spec: core.PodSpec{NodeName: "node-1"},
		Status: core.PodStatus{
			Phase: core.PodRunning,
			PodIP: "10.42.0.5",
			Conditions: []core.PodCondition{
				{Type: core.PodReady, Status: core.ConditionTrue},
			},
			ContainerStatuses: []core.ContainerStatus{
				{Name: "app", ContainerID: "containerd://abc123"},
			},
		},
	}
}

// nodeAgentTestPods returns n distinct Running & Ready pods that
// nodeAgentTargets will all select, each with its own name, PodIP, and
// resolved container ID for its "app" container.
func nodeAgentTestPods(ns string, n int) []*core.Pod {
	pods := make([]*core.Pod, n)
	for i := range n {
		pod := nodeAgentTestPod(ns, fmt.Sprintf("test-agent-pod-%d", i))
		pod.Status.PodIP = fmt.Sprintf("10.42.0.%d", i+10)
		pod.Status.ContainerStatuses[0].ContainerID = fmt.Sprintf("containerd://pod%d", i)
		pods[i] = pod
	}
	return pods
}

// podRuntimeObjects converts pods to the []runtime.Object shape
// fake.NewSimpleClientset takes as variadic seed objects.
func podRuntimeObjects(pods []*core.Pod) []runtime.Object {
	objs := make([]runtime.Object, len(pods))
	for i, pod := range pods {
		objs[i] = pod
	}
	return objs
}

// nodeAgentTestWorkload returns a Workload whose selector matches the pod
// that nodeAgentTestPod returns.
func nodeAgentTestWorkload(ns string) k8sapi.Workload {
	d := &apps.Deployment{
		ObjectMeta: meta.ObjectMeta{Name: "test-agent", Namespace: ns},
		Spec: apps.DeploymentSpec{
			Selector: &meta.LabelSelector{MatchLabels: map[string]string{"app": "test-agent"}},
		},
	}
	return k8sapi.Deployment(d)
}

// nodeAgentJobActionCounts tallies the create and delete actions ensureNodeAgent
// took against the Jobs resource, so a test can assert on them without
// depending on the fake clientset's exact Action type.
func nodeAgentJobActionCounts(actions []k8stesting.Action) (created, deleted int) {
	for _, a := range actions {
		if a.GetResource().Resource != "jobs" {
			continue
		}
		switch a.GetVerb() {
		case "create":
			created++
		case "delete":
			deleted++
		}
	}
	return created, deleted
}

// TestEnsureNodeAgent_NoCRISocket verifies that ensureNodeAgent provisions a
// node-agent Job when the traffic-manager has no container-runtime socket
// path configured (Helm value nodeAgent.criSocket unset): the Job then runs
// in auto-detect mode with the node's /run mounted.
func TestEnsureNodeAgent_NoCRISocket(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	cfg := testSidecar()
	pod := nodeAgentTestPod(cfg.Namespace, "test-agent-abc123")
	wl := nodeAgentTestWorkload(cfg.Namespace)

	ci := fake.NewSimpleClientset(pod)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs})

	s := newNodeAgentTestState()
	err := s.ensureNodeAgent(ctx, wl, cfg, true)
	require.NoError(t, err)

	created, _ := nodeAgentJobActionCounts(ci.Actions())
	assert.Equal(t, 1, created, "a job should be created in auto-detect mode when the CRI socket is unset")
}

// TestEnsureNodeAgent_ReusesHealthyExistingJob verifies that ensureNodeAgent
// treats a re-request for a still-healthy, identical Job as idempotent: it
// is neither deleted nor recreated.
func TestEnsureNodeAgent_ReusesHealthyExistingJob(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	cfg := testSidecar()
	pod := nodeAgentTestPod(cfg.Namespace, "test-agent-abc123")
	wl := nodeAgentTestWorkload(cfg.Namespace)

	existing, err := buildNodeAgentJob(cfg, nodeAgentJobOpts{
		namespace:    mgrNs,
		nodeName:     "node-1",
		containerIDs: map[string]string{"app": "containerd://abc123"},
		criSocket:    "/run/containerd/containerd.sock",
		podName:      pod.Name,
		podIP:        pod.Status.PodIP,
	})
	require.NoError(t, err)

	ci := fake.NewSimpleClientset(pod, existing)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs, NodeAgentCRISocket: "/run/containerd/containerd.sock"})

	s := newNodeAgentTestState()
	require.NoError(t, s.ensureNodeAgent(ctx, wl, cfg, true))

	created, deleted := nodeAgentJobActionCounts(ci.Actions())
	assert.Zero(t, deleted, "a healthy existing job must not be deleted")
	assert.Equal(t, 1, created, "only the initial (AlreadyExists) create should have been attempted")
}

// TestEnsureNodeAgent_ReplacesFailedJob verifies that ensureNodeAgent deletes
// and recreates an existing Job that has a JobFailed condition.
func TestEnsureNodeAgent_ReplacesFailedJob(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	cfg := testSidecar()
	pod := nodeAgentTestPod(cfg.Namespace, "test-agent-abc123")
	wl := nodeAgentTestWorkload(cfg.Namespace)

	existing, err := buildNodeAgentJob(cfg, nodeAgentJobOpts{
		namespace:    mgrNs,
		nodeName:     "node-1",
		containerIDs: map[string]string{"app": "containerd://abc123"},
		criSocket:    "/run/containerd/containerd.sock",
		podName:      pod.Name,
		podIP:        pod.Status.PodIP,
	})
	require.NoError(t, err)
	existing.Status.Conditions = []batchv1.JobCondition{
		{Type: batchv1.JobFailed, Status: core.ConditionTrue},
	}

	ci := fake.NewSimpleClientset(pod, existing)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs, NodeAgentCRISocket: "/run/containerd/containerd.sock"})

	s := newNodeAgentTestState()
	require.NoError(t, s.ensureNodeAgent(ctx, wl, cfg, true))

	jobs, err := ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)
	assert.Empty(t, jobs.Items[0].Status.Conditions, "the replacement must be a freshly created job, not the failed one")

	created, deleted := nodeAgentJobActionCounts(ci.Actions())
	assert.Equal(t, 1, deleted)
	assert.Equal(t, 2, created)
}

// TestEnsureNodeAgent_ReplacesTerminatingJob verifies that ensureNodeAgent
// replaces an existing Job that already has a DeletionTimestamp, waiting for
// it to disappear before creating the replacement.
func TestEnsureNodeAgent_ReplacesTerminatingJob(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	cfg := testSidecar()
	pod := nodeAgentTestPod(cfg.Namespace, "test-agent-abc123")
	wl := nodeAgentTestWorkload(cfg.Namespace)

	existing, err := buildNodeAgentJob(cfg, nodeAgentJobOpts{
		namespace:    mgrNs,
		nodeName:     "node-1",
		containerIDs: map[string]string{"app": "containerd://abc123"},
		criSocket:    "/run/containerd/containerd.sock",
		podName:      pod.Name,
		podIP:        pod.Status.PodIP,
	})
	require.NoError(t, err)
	now := meta.Now()
	existing.DeletionTimestamp = &now

	ci := fake.NewSimpleClientset(pod, existing)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs, NodeAgentCRISocket: "/run/containerd/containerd.sock"})

	s := newNodeAgentTestState()
	require.NoError(t, s.ensureNodeAgent(ctx, wl, cfg, true))

	jobs, err := ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)
	assert.Nil(t, jobs.Items[0].DeletionTimestamp, "the replacement must be a freshly created job")

	created, deleted := nodeAgentJobActionCounts(ci.Actions())
	assert.Equal(t, 1, deleted)
	assert.Equal(t, 2, created)
}

// TestEnsureNodeAgent_ReplacesJobWithDifferentContainerIDs verifies that
// ensureNodeAgent replaces an existing Job whose _TEL_NODE_AGENT_CONTAINER_IDS
// no longer matches the target, e.g. after a container restart invalidated
// the IDs the Job was built with.
func TestEnsureNodeAgent_ReplacesJobWithDifferentContainerIDs(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	cfg := testSidecar()
	pod := nodeAgentTestPod(cfg.Namespace, "test-agent-abc123")
	wl := nodeAgentTestWorkload(cfg.Namespace)

	existing, err := buildNodeAgentJob(cfg, nodeAgentJobOpts{
		namespace:    mgrNs,
		nodeName:     "node-1",
		containerIDs: map[string]string{"app": "containerd://stale999"},
		criSocket:    "/run/containerd/containerd.sock",
		podName:      pod.Name,
		podIP:        pod.Status.PodIP,
	})
	require.NoError(t, err)

	ci := fake.NewSimpleClientset(pod, existing)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs, NodeAgentCRISocket: "/run/containerd/containerd.sock"})

	s := newNodeAgentTestState()
	require.NoError(t, s.ensureNodeAgent(ctx, wl, cfg, true))

	jobs, err := ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1)
	idsEnv, ok := findEnv(jobs.Items[0].Spec.Template.Spec.Containers[0].Env, agentconfig.EnvNodeAgentContainerIDs)
	require.True(t, ok)
	assert.JSONEq(t, `{"app":"containerd://abc123"}`, idsEnv.Value)

	created, deleted := nodeAgentJobActionCounts(ci.Actions())
	assert.Equal(t, 1, deleted)
	assert.Equal(t, 2, created)
}

// TestNodeAgentJobName_NamespaceCollision verifies that a workload with the
// same name (and therefore the same generated pod name) in two different
// namespaces yields two different Job names, so the Jobs never collide.
func TestNodeAgentJobName_NamespaceCollision(t *testing.T) {
	t.Parallel()

	name1 := nodeAgentJobName("web", "ns1", "web-0")
	name2 := nodeAgentJobName("web", "ns2", "web-0")
	assert.NotEqual(t, name1, name2)
}

// TestNodeAgentTargets_AllReadyPods verifies that nodeAgentTargets returns
// one target for every Running & Ready pod of the workload, not just the
// first -- the basis for attaching a node-agent to every replica.
func TestNodeAgentTargets_AllReadyPods(t *testing.T) {
	t.Parallel()

	const ns = "test-namespace"
	wl := nodeAgentTestWorkload(ns)
	pods := nodeAgentTestPods(ns, 3)

	ci := fake.NewSimpleClientset(podRuntimeObjects(pods)...)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)

	targets, err := nodeAgentTargets(ctx, wl)
	require.NoError(t, err)
	names := make([]string, len(targets))
	for i, tgt := range targets {
		names[i] = tgt.podName
	}
	assert.ElementsMatch(t, []string{"test-agent-pod-0", "test-agent-pod-1", "test-agent-pod-2"}, names)
}

// TestNodeAgentTargets_SkipsInadmissiblePod verifies that a pod which fails
// checkNodeAgentTarget (here: one already carrying an injected traffic-agent,
// as an old-template pod mid-rollout might) is skipped rather than failing
// the whole request, while the other Running & Ready pods are still
// returned.
func TestNodeAgentTargets_SkipsInadmissiblePod(t *testing.T) {
	t.Parallel()

	const ns = "test-namespace"
	wl := nodeAgentTestWorkload(ns)
	pods := nodeAgentTestPods(ns, 3)
	pods[1].Spec.Containers = append(pods[1].Spec.Containers, core.Container{Name: agentconfig.ContainerName})

	ci := fake.NewSimpleClientset(podRuntimeObjects(pods)...)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)

	targets, err := nodeAgentTargets(ctx, wl)
	require.NoError(t, err)
	names := make([]string, len(targets))
	for i, tgt := range targets {
		names[i] = tgt.podName
	}
	assert.ElementsMatch(t, []string{"test-agent-pod-0", "test-agent-pod-2"}, names,
		"the inadmissible pod must be skipped, not fail the request for the rest")
}

// TestNodeAgentTargets_NoneReady verifies that nodeAgentTargets returns the
// generic "no running and ready pod" user error when no pod of the workload
// is even a candidate.
func TestNodeAgentTargets_NoneReady(t *testing.T) {
	t.Parallel()

	const ns = "test-namespace"
	wl := nodeAgentTestWorkload(ns)
	pod := nodeAgentTestPod(ns, "test-agent-pod-0")
	pod.Status.Phase = core.PodPending
	pod.Status.Conditions = nil

	ci := fake.NewSimpleClientset(pod)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)

	_, err := nodeAgentTargets(ctx, wl)
	require.Error(t, err)
	assert.Equal(t, errcat.User, errcat.GetCategory(err))
	assert.Contains(t, err.Error(), "no running and ready pod available")
}

// TestNodeAgentTargets_AllInadmissible verifies that when every Running &
// Ready pod fails checkNodeAgentTarget, nodeAgentTargets fails with the
// precise cause (rather than the generic "no pod" message), since it is the
// more useful error when the workload does have live pods.
func TestNodeAgentTargets_AllInadmissible(t *testing.T) {
	t.Parallel()

	const ns = "test-namespace"
	wl := nodeAgentTestWorkload(ns)
	pods := nodeAgentTestPods(ns, 2)
	for _, pod := range pods {
		pod.Spec.HostNetwork = true
	}

	ci := fake.NewSimpleClientset(podRuntimeObjects(pods)...)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)

	_, err := nodeAgentTargets(ctx, wl)
	require.Error(t, err)
	assert.Equal(t, errcat.User, errcat.GetCategory(err))
	assert.Contains(t, err.Error(), "host networking")
}

// TestEnsureNodeAgent_AllReplicas_CreatesOneJobPerPod verifies the intercept
// fan-out: ensureNodeAgent(allReplicas=true) creates one Job per Running &
// Ready pod, each with the pod's own container IDs and PodIP baked into its
// env and a deterministic name derived from that pod, and that a second
// request is idempotent (no duplicate Jobs).
func TestEnsureNodeAgent_AllReplicas_CreatesOneJobPerPod(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	cfg := testSidecar()
	wl := nodeAgentTestWorkload(cfg.Namespace)
	pods := nodeAgentTestPods(cfg.Namespace, 3)

	ci := fake.NewSimpleClientset(podRuntimeObjects(pods)...)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs, NodeAgentCRISocket: "/run/containerd/containerd.sock"})

	s := newNodeAgentTestState()
	require.NoError(t, s.ensureNodeAgent(ctx, wl, cfg, true))

	jobs, err := ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 3, "one job per pod")

	byPod := make(map[string]*batchv1.Job, len(jobs.Items))
	for i := range jobs.Items {
		job := &jobs.Items[i]
		byPod[job.Labels[nodeAgentTargetPodLabel]] = job
	}
	for i, pod := range pods {
		job, ok := byPod[pod.Name]
		require.True(t, ok, "no job found for pod %s", pod.Name)
		assert.Equal(t, nodeAgentJobName(cfg.AgentName, cfg.Namespace, pod.Name), job.Name)

		idsEnv, ok := findEnv(job.Spec.Template.Spec.Containers[0].Env, agentconfig.EnvNodeAgentContainerIDs)
		require.True(t, ok)
		assert.JSONEq(t, fmt.Sprintf(`{"app":"containerd://pod%d"}`, i), idsEnv.Value)

		podIPEnv, ok := findEnv(job.Spec.Template.Spec.Containers[0].Env, agentconfig.EnvNodeAgentPodIP)
		require.True(t, ok)
		assert.Equal(t, pod.Status.PodIP, podIPEnv.Value)
	}

	// A re-request must be idempotent per Job: no duplicates.
	require.NoError(t, s.ensureNodeAgent(ctx, wl, cfg, true))
	jobs, err = ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	assert.Len(t, jobs.Items, 3, "re-request must not create duplicate jobs")
}

// TestEnsureNodeAgent_Ingest_CreatesOnlyOneJob verifies that
// ensureNodeAgent(allReplicas=false) creates exactly one Job even though
// several pods are admissible: an ingest reads env and mounts from a single
// pod and gains nothing from the others.
func TestEnsureNodeAgent_Ingest_CreatesOnlyOneJob(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	cfg := testSidecar()
	wl := nodeAgentTestWorkload(cfg.Namespace)
	pods := nodeAgentTestPods(cfg.Namespace, 3)

	ci := fake.NewSimpleClientset(podRuntimeObjects(pods)...)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs, NodeAgentCRISocket: "/run/containerd/containerd.sock"})

	s := newNodeAgentTestState()
	require.NoError(t, s.ensureNodeAgent(ctx, wl, cfg, false))

	jobs, err := ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1, "an ingest needs only one job even though every pod is admissible")
}

// TestEnsureNodeAgent_Ingest_ReusesJobForAdmissibleTarget verifies that
// ensureNodeAgent(allReplicas=false) does nothing when a live Job already
// targets a pod that is still admissible, instead of creating a second one.
func TestEnsureNodeAgent_Ingest_ReusesJobForAdmissibleTarget(t *testing.T) {
	t.Parallel()

	const mgrNs = "ambassador"
	cfg := testSidecar()
	wl := nodeAgentTestWorkload(cfg.Namespace)
	pods := nodeAgentTestPods(cfg.Namespace, 2)

	existing, err := buildNodeAgentJob(cfg, nodeAgentJobOpts{
		namespace:    mgrNs,
		nodeName:     "node-1",
		containerIDs: map[string]string{"app": "containerd://pod1"},
		criSocket:    "/run/containerd/containerd.sock",
		podName:      pods[1].Name,
		podIP:        pods[1].Status.PodIP,
	})
	require.NoError(t, err)

	objs := append(podRuntimeObjects(pods), existing)
	ci := fake.NewSimpleClientset(objs...)
	ctx := k8sapi.WithK8sInterface(t.Context(), ci)
	ctx = managerutil.WithEnv(ctx, &managerutil.Env{ManagerNamespace: mgrNs, NodeAgentCRISocket: "/run/containerd/containerd.sock"})

	s := newNodeAgentTestState()
	require.NoError(t, s.ensureNodeAgent(ctx, wl, cfg, false))

	jobs, err := ci.BatchV1().Jobs(mgrNs).List(context.Background(), meta.ListOptions{})
	require.NoError(t, err)
	require.Len(t, jobs.Items, 1, "a live job for a still-admissible pod must be reused, not duplicated")
	assert.Equal(t, existing.Name, jobs.Items[0].Name)

	created, _ := nodeAgentJobActionCounts(ci.Actions())
	assert.Zero(t, created, "nothing should be created when a still-admissible job already exists")
}

package state

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
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
		podName:      "test-agent-abc123",
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

	idsEnv, ok := findEnv(env, envNodeAgentContainerIDs)
	require.True(t, ok)
	var decoded map[string]string
	require.NoError(t, json.Unmarshal([]byte(idsEnv.Value), &decoded))
	assert.Equal(t, opts.containerIDs, decoded)

	criEnv, ok := findEnv(env, envNodeAgentCRISocket)
	require.True(t, ok)
	assert.Equal(t, "/run/containerd/containerd.sock", criEnv.Value)
}

// TestBuildNodeAgentJob_NoCRISocket verifies that the CRI socket env var and
// its hostPath volume/mount are omitted when no socket path is configured,
// leaving the node-agent to fall back to cri.DetectSocket.
func TestBuildNodeAgentJob_NoCRISocket(t *testing.T) {
	t.Parallel()

	opts := testOpts()
	opts.criSocket = ""
	job, err := buildNodeAgentJob(testSidecar(), opts)
	require.NoError(t, err)

	env := job.Spec.Template.Spec.Containers[0].Env
	_, ok := findEnv(env, envNodeAgentCRISocket)
	assert.False(t, ok, "CRI socket env var should be absent when criSocket is empty")

	for _, v := range job.Spec.Template.Spec.Volumes {
		assert.NotEqual(t, nodeAgentCRIVolumeName, v.Name, "CRI socket volume should be absent when criSocket is empty")
	}
	for _, m := range job.Spec.Template.Spec.Containers[0].VolumeMounts {
		assert.NotEqual(t, nodeAgentCRIVolumeName, m.Name, "CRI socket mount should be absent when criSocket is empty")
	}
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

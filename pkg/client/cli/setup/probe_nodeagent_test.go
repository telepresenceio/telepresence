package setup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func containerdNode(name string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: map[string]string{"kubernetes.io/os": "linux"},
		},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{ContainerRuntimeVersion: "containerd://1.6.6"},
		},
	}
}

func TestProbeNodeAgent_HappyPath(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*authv1.ResourceAttributes) bool { return true })
	var sawDryRun bool
	client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if ca, ok := action.(k8stesting.CreateActionImpl); ok {
			opts := ca.GetCreateOptions()
			sawDryRun = len(opts.DryRun) == 1 && opts.DryRun[0] == metav1.DryRunAll
		}
		return false, nil, nil // let the default reactor chain handle the actual creation
	})

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeNodeAgent(context.Background(), []corev1.Node{containerdNode("node-1")}, nil, "", true)

	require.Equal(t, VerdictYes, facts.Viable.Verdict)
	assert.True(t, sawDryRun, "expected the canary create to carry DryRunAll")
	assert.Equal(t, 1, facts.LinuxNodes)
	assert.Equal(t, 1, facts.TotalNodes)
	assert.Equal(t, []string{"containerd"}, facts.Runtimes)
}

func TestProbeNodeAgent_CanaryForbidden(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(*authv1.ResourceAttributes) bool { return true })
	client.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("PodSecurity policy violation: hostPID"))
	})

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeNodeAgent(context.Background(), []corev1.Node{containerdNode("node-1")}, nil, "", true)

	require.Equal(t, VerdictNo, facts.Viable.Verdict)
	assert.Contains(t, facts.CanaryDenial, "PodSecurity")
}

func TestProbeNodeAgent_Autopilot(t *testing.T) {
	client := fake.NewClientset()
	node := containerdNode("gk3-cluster-node-1")
	node.Labels["cloud.google.com/gke-autopilot"] = "true"

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeNodeAgent(context.Background(), []corev1.Node{node}, nil, "gke", true)

	require.Equal(t, VerdictNo, facts.Viable.Verdict)
	assert.True(t, facts.Autopilot)
	assert.Contains(t, facts.Viable.Evidence[0], "Autopilot")
}

func TestProbeNodeAgent_CanaryRBACDenied(t *testing.T) {
	client := fake.NewClientset()
	k8sapi.InstallFakeSelfSubjectAccessReviews(client, func(ra *authv1.ResourceAttributes) bool {
		return ra.Resource != "pods"
	})

	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeNodeAgent(context.Background(), []corev1.Node{containerdNode("node-1")}, nil, "", true)

	require.Equal(t, VerdictProbable, facts.Viable.Verdict)
	assert.Contains(t, facts.Viable.Evidence[0], "insufficient RBAC")
	assert.Empty(t, facts.CanaryDenial)
}

func TestProbeNodeAgent_ManagerNamespaceMissing(t *testing.T) {
	client := fake.NewClientset()
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeNodeAgent(context.Background(), []corev1.Node{containerdNode("node-1")}, nil, "", false)

	require.Equal(t, VerdictProbable, facts.Viable.Verdict)
	assert.Contains(t, facts.Viable.Evidence[0], "canary skipped")
}

func TestProbeNodeAgent_NodesDenied(t *testing.T) {
	client := fake.NewClientset()
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeNodeAgent(context.Background(), nil, errors.New("denied"), "", true)
	assert.Equal(t, VerdictUnknown, facts.Viable.Verdict)
}

func TestRuntimeSupport(t *testing.T) {
	supported, evidence := runtimeSupport([]string{"containerd", "cri-o"})
	assert.True(t, supported)
	assert.Empty(t, evidence)

	supported, evidence = runtimeSupport([]string{"docker"})
	assert.False(t, supported)
	assert.NotEmpty(t, evidence)

	supported, evidence = runtimeSupport([]string{"mystery"})
	assert.False(t, supported)
	assert.NotEmpty(t, evidence)
}

package setup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestProbeQuic_LoadBalancerWithIngress(t *testing.T) {
	client := fake.NewClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "traffic-manager-quic", Namespace: "ambassador"},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
		Status: corev1.ServiceStatus{
			LoadBalancer: corev1.LoadBalancerStatus{
				Ingress: []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}},
			},
		},
	})
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeQuic(context.Background(), nil, nil, "unknown")
	require.Equal(t, VerdictYes, facts.LoadBalancer.Verdict)
	assert.NotEmpty(t, facts.LoadBalancer.Evidence)
}

func TestProbeQuic_CloudProviderNoLBService(t *testing.T) {
	client := fake.NewClientset()
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeQuic(context.Background(), nil, nil, "gke")
	assert.Equal(t, "gke", facts.Provider)
	require.Equal(t, VerdictProbable, facts.LoadBalancer.Verdict)
}

func TestProbeQuic_KindProvider(t *testing.T) {
	client := fake.NewClientset()
	nodes := []corev1.Node{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "kind-control-plane"},
			Status: corev1.NodeStatus{
				Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.18.0.2"}},
			},
		},
	}
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeQuic(context.Background(), nodes, nil, "kind")

	require.Equal(t, VerdictNo, facts.LoadBalancer.Verdict)
	require.Equal(t, VerdictProbable, facts.NodePort.Verdict)
}

func TestProbeQuic_NodesDenied(t *testing.T) {
	client := fake.NewClientset()
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	facts := p.probeQuic(context.Background(), nil, errors.New("denied"), "unknown")
	assert.Equal(t, VerdictUnknown, facts.NodePort.Verdict)
}

func TestClassifyProvider_SkipsUnrecognizedSchemes(t *testing.T) {
	nodes := []corev1.Node{
		{Spec: corev1.NodeSpec{ProviderID: "virtual-node://edge/node-1"}},
		{Spec: corev1.NodeSpec{ProviderID: "gce://proj/us-central1-a/node-2"}},
	}
	assert.Equal(t, "gke", classifyProvider(nodes))
	assert.Equal(t, "unknown", classifyProvider(nodes[:1]))
	assert.Equal(t, "unknown", classifyProvider(nil))
}

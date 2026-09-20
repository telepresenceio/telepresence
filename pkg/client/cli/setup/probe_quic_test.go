package setup

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestProbeQuic_LoadBalancerWithIngress(t *testing.T) {
	client := fake.NewClientset(&core.Service{
		ObjectMeta: meta.ObjectMeta{Name: "traffic-manager-quic", Namespace: "ambassador"},
		Spec:       core.ServiceSpec{Type: core.ServiceTypeLoadBalancer},
		Status: core.ServiceStatus{
			LoadBalancer: core.LoadBalancerStatus{
				Ingress: []core.LoadBalancerIngress{{IP: "1.2.3.4"}},
			},
		},
	})
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	services, listEvidence := p.listServices(context.Background())
	facts := p.probeQuic(nil, nil, "unknown", services, listEvidence)
	require.Equal(t, VerdictYes, facts.LoadBalancer.Verdict)
	assert.NotEmpty(t, facts.LoadBalancer.Evidence)
}

func TestProbeQuic_CloudProviderNoLBService(t *testing.T) {
	client := fake.NewClientset()
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	services, listEvidence := p.listServices(context.Background())
	facts := p.probeQuic(nil, nil, "gke", services, listEvidence)
	assert.Equal(t, "gke", facts.Provider)
	require.Equal(t, VerdictProbable, facts.LoadBalancer.Verdict)
}

func TestProbeQuic_KindProvider(t *testing.T) {
	client := fake.NewClientset()
	nodes := []core.Node{
		{
			ObjectMeta: meta.ObjectMeta{Name: "kind-control-plane"},
			Status: core.NodeStatus{
				Addresses: []core.NodeAddress{{Type: core.NodeInternalIP, Address: "172.18.0.2"}},
			},
		},
	}
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	services, listEvidence := p.listServices(context.Background())
	facts := p.probeQuic(nodes, nil, "kind", services, listEvidence)

	require.Equal(t, VerdictNo, facts.LoadBalancer.Verdict)
	require.Equal(t, VerdictProbable, facts.NodePort.Verdict)
}

func TestProbeQuic_NodesDenied(t *testing.T) {
	client := fake.NewClientset()
	p := &Prober{KubeClient: client, ManagerNamespace: "ambassador"}
	services, listEvidence := p.listServices(context.Background())
	facts := p.probeQuic(nil, errors.New("denied"), "unknown", services, listEvidence)
	assert.Equal(t, VerdictUnknown, facts.NodePort.Verdict)
}

func TestClassifyProvider_SkipsUnrecognizedSchemes(t *testing.T) {
	nodes := []core.Node{
		{Spec: core.NodeSpec{ProviderID: "virtual-node://edge/node-1"}},
		{Spec: core.NodeSpec{ProviderID: "gce://proj/us-central1-a/node-2"}},
	}
	assert.Equal(t, "gke", classifyProvider(nodes))
	assert.Equal(t, "unknown", classifyProvider(nodes[:1]))
	assert.Equal(t, "unknown", classifyProvider(nil))
}

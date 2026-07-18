package quictunnel

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authv1 "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes/fake"

	fakeargorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned/fake"
	"github.com/telepresenceio/clog/testutil"
	"github.com/telepresenceio/telepresence/v2/pkg/informer"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

func lbService(ingress ...core.LoadBalancerIngress) *core.Service {
	return &core.Service{
		Spec: core.ServiceSpec{
			Type:  core.ServiceTypeLoadBalancer,
			Ports: []core.ServicePort{{Port: 7778}},
		},
		Status: core.ServiceStatus{
			LoadBalancer: core.LoadBalancerStatus{Ingress: ingress},
		},
	}
}

func nodePortService(nodePort int32) *core.Service {
	return &core.Service{
		Spec: core.ServiceSpec{
			Type:  core.ServiceTypeNodePort,
			Ports: []core.ServicePort{{Port: 7778, NodePort: nodePort}},
		},
	}
}

func node(name string, addrs ...core.NodeAddress) *core.Node {
	return &core.Node{
		ObjectMeta: meta.ObjectMeta{Name: name},
		Status:     core.NodeStatus{Addresses: addrs},
	}
}

func TestCandidatesForService_LoadBalancerIP(t *testing.T) {
	svc := lbService(core.LoadBalancerIngress{IP: "203.0.113.9"})
	cands := candidatesForService(svc, nil)
	assert.Equal(t, []Candidate{{Host: "203.0.113.9", Port: 7778}}, cands)
}

func TestCandidatesForService_LoadBalancerHostname(t *testing.T) {
	svc := lbService(core.LoadBalancerIngress{Hostname: "quic.example.com"})
	cands := candidatesForService(svc, nil)
	assert.Equal(t, []Candidate{{Host: "quic.example.com", Port: 7778}}, cands)
}

func TestCandidatesForService_LoadBalancerMultipleIngress_PreservesOrder(t *testing.T) {
	svc := lbService(
		core.LoadBalancerIngress{IP: "203.0.113.9"},
		core.LoadBalancerIngress{Hostname: "quic.example.com"},
	)
	cands := candidatesForService(svc, nil)
	assert.Equal(t, []Candidate{
		{Host: "203.0.113.9", Port: 7778},
		{Host: "quic.example.com", Port: 7778},
	}, cands)
}

func TestCandidatesForService_LoadBalancerUnassigned(t *testing.T) {
	svc := lbService()
	assert.Empty(t, candidatesForService(svc, nil))
}

func TestCandidatesForService_NodePort_ExternalIPPreferred(t *testing.T) {
	svc := nodePortService(31778)
	nodes := []*core.Node{
		node("node-a",
			core.NodeAddress{Type: core.NodeInternalIP, Address: "10.0.0.1"},
			core.NodeAddress{Type: core.NodeExternalIP, Address: "198.51.100.1"},
		),
	}
	cands := candidatesForService(svc, nodes)
	assert.Equal(t, []Candidate{{Host: "198.51.100.1", Port: 31778}}, cands)
}

func TestCandidatesForService_NodePort_InternalIPFallback(t *testing.T) {
	// kind nodes have no ExternalIP; this is the branch the NodePort integration
	// test exercises against a real kind cluster.
	svc := nodePortService(31778)
	nodes := []*core.Node{
		node("node-a", core.NodeAddress{Type: core.NodeInternalIP, Address: "172.18.0.2"}),
	}
	cands := candidatesForService(svc, nodes)
	assert.Equal(t, []Candidate{{Host: "172.18.0.2", Port: 31778}}, cands)
}

func TestCandidatesForService_NodePort_SkipsNodeWithNoUsableAddress(t *testing.T) {
	svc := nodePortService(31778)
	nodes := []*core.Node{
		node("node-a"), // no addresses at all
		node("node-b", core.NodeAddress{Type: core.NodeInternalIP, Address: "172.18.0.3"}),
	}
	cands := candidatesForService(svc, nodes)
	assert.Equal(t, []Candidate{{Host: "172.18.0.3", Port: 31778}}, cands)
}

func TestCandidatesForService_NodePort_NoNodeAccess(t *testing.T) {
	// nodes == nil is Discovery's encoding of "node access unavailable, or not yet
	// listed" -- see the Forbidden-nodes case in Discovery.Start.
	svc := nodePortService(31778)
	assert.Empty(t, candidatesForService(svc, nil))
}

func TestCandidatesForService_NodePort_ZeroPort(t *testing.T) {
	svc := nodePortService(0)
	nodes := []*core.Node{node("node-a", core.NodeAddress{Type: core.NodeInternalIP, Address: "172.18.0.2"})}
	assert.Empty(t, candidatesForService(svc, nodes))
}

func TestCandidatesForService_ClusterIP(t *testing.T) {
	svc := &core.Service{Spec: core.ServiceSpec{Type: core.ServiceTypeClusterIP, Ports: []core.ServicePort{{Port: 7778}}}}
	assert.Empty(t, candidatesForService(svc, nil))
}

func TestCandidatesForService_Nil(t *testing.T) {
	assert.Empty(t, candidatesForService(nil, nil))
}

func TestCandidatesForService_CapsAtMaxCandidates(t *testing.T) {
	svc := nodePortService(31778)
	var nodes []*core.Node
	for i := range maxCandidates + 5 {
		nodes = append(nodes, node(string(rune('a'+i)), core.NodeAddress{Type: core.NodeInternalIP, Address: "10.0.0.1"}))
	}
	cands := candidatesForService(svc, nodes)
	assert.Len(t, cands, maxCandidates)
}

// TestDiscovery_Start_LoadBalancer proves the end-to-end wiring: an initial Get sees
// the Service as it exists when Start is called, and a later watch event (the cloud
// provider assigning an ingress address after the fact, the documented "not
// advertised until assignment" case) is picked up without a further Start call.
func TestDiscovery_Start_LoadBalancer(t *testing.T) {
	// The fake clientset doesn't support the WatchListClient feature (no bookmark
	// events), which is enabled by default in client-go v0.35+. Disable it so the
	// Node informer's cache sync doesn't hang.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: "traffic-manager-quic", Namespace: "ambassador"},
		Spec: core.ServiceSpec{
			Type:  core.ServiceTypeLoadBalancer,
			Ports: []core.ServicePort{{Port: 7778}},
		},
	}
	fakeClient := fake.NewClientset(svc)
	k8sapi.InstallFakeSelfSubjectAccessReviews(fakeClient, nil)
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, fakeClient, fakeargorollouts.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")

	d := NewDiscovery()
	d.Start(ctx, "ambassador", "traffic-manager-quic")
	req.Empty(d.Candidates(), "no ingress assigned yet: not advertised")

	svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{IP: "203.0.113.9"}}
	_, err := fakeClient.CoreV1().Services("ambassador").UpdateStatus(ctx, svc, meta.UpdateOptions{})
	req.NoError(err)

	req.Eventually(func() bool {
		cands := d.Candidates()
		return len(cands) == 1 && cands[0] == Candidate{Host: "203.0.113.9", Port: 7778}
	}, 2*time.Second, 10*time.Millisecond, "candidate list must reflect the later ingress assignment")
}

// TestDiscovery_Start_NodePortWithoutNodeAccess proves the documented degradation: a
// NodePort Service with no node list/watch RBAC (InstallFakeSelfSubjectAccessReviews
// with allowed == nil denies everything, matching a namespace-scoped install)
// advertises no candidates instead of erroring.
func TestDiscovery_Start_NodePortWithoutNodeAccess(t *testing.T) {
	// The fake clientset doesn't support the WatchListClient feature (no bookmark
	// events), which is enabled by default in client-go v0.35+. Disable it so the
	// Node informer's cache sync doesn't hang.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: "traffic-manager-quic", Namespace: "ambassador"},
		Spec: core.ServiceSpec{
			Type:  core.ServiceTypeNodePort,
			Ports: []core.ServicePort{{Port: 7778, NodePort: 31778}},
		},
	}
	fakeClient := fake.NewClientset(svc,
		&core.Node{
			ObjectMeta: meta.ObjectMeta{Name: "kind-worker"},
			Status:     core.NodeStatus{Addresses: []core.NodeAddress{{Type: core.NodeInternalIP, Address: "172.18.0.2"}}},
		},
	)
	k8sapi.InstallFakeSelfSubjectAccessReviews(fakeClient, nil) // deny everything
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, fakeClient, fakeargorollouts.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")

	d := NewDiscovery()
	d.Start(ctx, "ambassador", "traffic-manager-quic")

	req.Empty(d.Candidates(), "NodePort without node RBAC must advertise nothing, not the node-less port")
}

// TestDiscovery_Start_NodePortWithNodeAccess proves the happy path this phase exists
// for: a NodePort Service plus permitted Node access resolves to the node's
// InternalIP, exactly as the kind integration test expects (kind nodes have no
// ExternalIP).
func TestDiscovery_Start_NodePortWithNodeAccess(t *testing.T) {
	// The fake clientset doesn't support the WatchListClient feature (no bookmark
	// events), which is enabled by default in client-go v0.35+. Disable it so the
	// Node informer's cache sync doesn't hang.
	clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, false)
	ctx := testutil.NewContext(t, true)
	req := require.New(t)

	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: "traffic-manager-quic", Namespace: "ambassador"},
		Spec: core.ServiceSpec{
			Type:  core.ServiceTypeNodePort,
			Ports: []core.ServicePort{{Port: 7778, NodePort: 31778}},
		},
	}
	fakeClient := fake.NewClientset(svc,
		&core.Node{
			ObjectMeta: meta.ObjectMeta{Name: "kind-worker"},
			Status:     core.NodeStatus{Addresses: []core.NodeAddress{{Type: core.NodeInternalIP, Address: "172.18.0.2"}}},
		},
	)
	k8sapi.InstallFakeSelfSubjectAccessReviews(fakeClient, func(*authv1.ResourceAttributes) bool { return true })
	ctx = k8sapi.WithJoinedClientSetInterface(ctx, fakeClient, fakeargorollouts.NewSimpleClientset())
	ctx = informer.WithFactory(ctx, "")

	d := NewDiscovery()
	d.Start(ctx, "ambassador", "traffic-manager-quic")

	req.Eventually(func() bool {
		cands := d.Candidates()
		return len(cands) == 1 && cands[0] == Candidate{Host: "172.18.0.2", Port: 31778}
	}, 2*time.Second, 10*time.Millisecond)
}

package setup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestResolveServiceDialAddr covers the LoadBalancer/NodePort/ClusterIP
// classification shared by quicDialAddr and externalDialAddr, independent of
// either caller's port name or error-message label.
func TestResolveServiceDialAddr(t *testing.T) {
	const portName = "probe"
	const label = "the probe service"

	t.Run("LoadBalancer with an ingress IP", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Name: portName, Port: 9999}},
		}}
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
		addr, err := resolveServiceDialAddr(context.Background(), fake.NewClientset(), svc, portName, label)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3.4:9999", addr)
	})

	t.Run("LoadBalancer with an ingress hostname", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Name: portName, Port: 9999}},
		}}
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}}
		addr, err := resolveServiceDialAddr(context.Background(), fake.NewClientset(), svc, portName, label)
		require.NoError(t, err)
		assert.Equal(t, "lb.example.com:9999", addr)
	})

	t.Run("NodePort needs a node address", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeNodePort,
			Ports: []corev1.ServicePort{{Name: portName, Port: 9999, NodePort: 31999}},
		}}
		client := fake.NewClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeExternalIP, Address: "203.0.113.9"},
			}},
		})
		addr, err := resolveServiceDialAddr(context.Background(), client, svc, portName, label)
		require.NoError(t, err)
		assert.Equal(t, "203.0.113.9:31999", addr)
	})

	t.Run("ClusterIP has no externally reachable address", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}}
		_, err := resolveServiceDialAddr(context.Background(), fake.NewClientset(), svc, portName, label)
		assert.Error(t, err)
	})
}

// TestServicePortByName covers the shared port-lookup mechanics used for
// both the QUIC and external endpoint services.
func TestServicePortByName(t *testing.T) {
	t.Run("named port found", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Name: "health", Port: 8080}, {Name: "probe", Port: 9999}},
		}}
		p, ok := servicePortByName(svc, "probe")
		require.True(t, ok)
		assert.Equal(t, int32(9999), p.Port)
	})
	t.Run("falls back to the sole port", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9999}}}}
		p, ok := servicePortByName(svc, "probe")
		require.True(t, ok)
		assert.Equal(t, int32(9999), p.Port)
	})
	t.Run("no match among several unnamed ports", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Name: "a", Port: 1}, {Name: "b", Port: 2}},
		}}
		_, ok := servicePortByName(svc, "probe")
		assert.False(t, ok)
	})
}

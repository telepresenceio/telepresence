package setup

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func quicValuesEnabled() map[string]any {
	return map[string]any{"quicTunnel": map[string]any{"enabled": true}}
}

func injectorValuesEnabled() map[string]any {
	return map[string]any{"agentInjector": map[string]any{"enabled": true}}
}

func quicService(svcType corev1.ServiceType, mod ...func(*corev1.Service)) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: quicServiceName, Namespace: "ambassador"},
		Spec:       corev1.ServiceSpec{Type: svcType},
	}
	for _, m := range mod {
		m(svc)
	}
	return svc
}

// shortCtx bounds the LoadBalancer polling well below its 2s interval so a
// no-ingress test concludes on the first check.
func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestVerifyInstall_QuicLoadBalancer(t *testing.T) {
	t.Run("assigned ingress", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
		}))
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled())
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "QUIC endpoint available at 1.2.3.4")
	})
	t.Run("hostname ingress", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}}
		}))
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled())
		require.Len(t, notes, 1)
		assert.Contains(t, notes[0].Text, "lb.example.com")
	})
	t.Run("no ingress within the deadline", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeLoadBalancer))
		notes := VerifyInstall(shortCtx(t), client, "ambassador", quicValuesEnabled())
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "no QUIC endpoint yet")
	})
}

func TestVerifyInstall_QuicNodePort(t *testing.T) {
	t.Run("allocated node port", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Name: "quic", Port: 7778, NodePort: 31234}}
		}))
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled())
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "31234")
	})
	t.Run("no node port allocated", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Name: "quic", Port: 7778}}
		}))
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled())
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
	})
}

func TestVerifyInstall_QuicServiceMissingOrUnreachable(t *testing.T) {
	t.Run("service missing", func(t *testing.T) {
		client := fake.NewClientset()
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled())
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "was not found")
	})
	t.Run("ClusterIP service", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeClusterIP))
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled())
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "ClusterIP")
	})
}

func injectorSlice(ready bool) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      injectorServiceName + "-abc",
			Namespace: "ambassador",
			Labels:    map[string]string{discoveryv1.LabelServiceName: injectorServiceName},
		},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.5"},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			},
		},
	}
}

func TestVerifyInstall_InjectorService(t *testing.T) {
	t.Run("ready endpoints", func(t *testing.T) {
		client := fake.NewClientset(injectorSlice(true))
		notes := VerifyInstall(context.Background(), client, "ambassador", injectorValuesEnabled())
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "ready endpoints")
	})
	t.Run("no ready endpoints", func(t *testing.T) {
		client := fake.NewClientset(injectorSlice(false))
		notes := VerifyInstall(context.Background(), client, "ambassador", injectorValuesEnabled())
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "failurePolicy Ignore")
	})
	t.Run("endpoints missing", func(t *testing.T) {
		client := fake.NewClientset()
		notes := VerifyInstall(context.Background(), client, "ambassador", injectorValuesEnabled())
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
	})
}

func TestVerifyInstall_NothingEnabled(t *testing.T) {
	client := fake.NewClientset()
	notes := VerifyInstall(context.Background(), client, "ambassador", map[string]any{
		"quicTunnel":    map[string]any{"enabled": false},
		"agentInjector": map[string]any{"enabled": false},
	})
	assert.Empty(t, notes)
}

func TestApply_NoneIsNoOp(t *testing.T) {
	out := &bytes.Buffer{}
	require.NoError(t, Apply(context.Background(), nil, "ambassador", &Proposal{Action: ActionNone}, out))
	assert.Empty(t, out.String())
}

func TestApplyOutcome(t *testing.T) {
	assert.Equal(t, "installed", ApplyOutcome(ActionInstall))
	assert.Equal(t, "upgraded", ApplyOutcome(ActionUpgrade))
}

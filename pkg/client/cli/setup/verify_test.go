package setup

import (
	"bytes"
	"context"
	"crypto/tls"
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
	return map[string]any{
		"quicTunnel":    map[string]any{"enabled": true},
		"agentInjector": map[string]any{"enabled": false},
	}
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

// fakeDialSuccess is a quicDialer stand-in for a completed handshake.
func fakeDialSuccess(context.Context, string, *tls.Config) error { return nil }

func TestVerifyInstall_QuicLoadBalancer(t *testing.T) {
	t.Run("assigned ingress, reachable", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
			svc.Spec.Ports = []corev1.ServicePort{{Name: "quic", Port: 7778}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "QUIC endpoint reachable from this workstation at 1.2.3.4:7778")
	})
	t.Run("hostname ingress, reachable", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}}
			svc.Spec.Ports = []corev1.ServicePort{{Name: "quic", Port: 7778}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess)
		require.Len(t, notes, 1)
		assert.Contains(t, notes[0].Text, "lb.example.com:7778")
	})
	t.Run("no ingress within the deadline", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeLoadBalancer))
		notes := VerifyInstall(shortCtx(t), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "no QUIC endpoint yet")
	})
	t.Run("assigned ingress, port not identifiable", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "dial address could not be determined")
	})
	t.Run("assigned ingress, not reachable", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeLoadBalancer, func(svc *corev1.Service) {
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
			svc.Spec.Ports = []corev1.ServicePort{{Name: "quic", Port: 7778}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{},
			func(ctx context.Context, addr string, tlsConf *tls.Config) error { return context.DeadlineExceeded })
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "not reachable over UDP")
	})
}

func TestVerifyInstall_QuicNodePort(t *testing.T) {
	t.Run("allocated node port, reachable", func(t *testing.T) {
		client := fake.NewClientset(
			quicService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
				svc.Spec.Ports = []corev1.ServicePort{{Name: "quic", Port: 7778, NodePort: 31234}}
			}),
			&corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: "node1"},
				Status: corev1.NodeStatus{
					Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "172.18.0.2"}},
				},
			},
		)
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "reachable from this workstation at 172.18.0.2:31234")
	})
	t.Run("allocated node port, no node address", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Name: "quic", Port: 7778, NodePort: 31234}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "dial address could not be determined")
	})
	t.Run("no node port allocated", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeNodePort, func(svc *corev1.Service) {
			svc.Spec.Ports = []corev1.ServicePort{{Name: "quic", Port: 7778}}
		}))
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
	})
}

func TestVerifyInstall_QuicServiceMissingOrUnreachable(t *testing.T) {
	t.Run("service missing", func(t *testing.T) {
		client := fake.NewClientset()
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "was not found")
	})
	t.Run("ClusterIP service", func(t *testing.T) {
		client := fake.NewClientset(quicService(corev1.ServiceTypeClusterIP))
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{})
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
		notes := VerifyInstall(context.Background(), client, "ambassador", injectorValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "ready endpoints")
	})
	t.Run("no ready endpoints", func(t *testing.T) {
		client := fake.NewClientset(injectorSlice(false))
		notes := VerifyInstall(context.Background(), client, "ambassador", injectorValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "failurePolicy Ignore")
	})
	t.Run("endpoints missing", func(t *testing.T) {
		client := fake.NewClientset()
		notes := VerifyInstall(context.Background(), client, "ambassador", injectorValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
	})
	t.Run("injector absent from the values runs the check", func(t *testing.T) {
		client := fake.NewClientset(injectorSlice(true))
		notes := VerifyInstall(context.Background(), client, "ambassador", map[string]any{}, ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "ready endpoints")
	})
	t.Run("custom injector name is honored", func(t *testing.T) {
		slice := injectorSlice(true)
		slice.Labels[discoveryv1.LabelServiceName] = "my-injector"
		client := fake.NewClientset(slice)
		values := map[string]any{"agentInjector": map[string]any{"name": "my-injector"}}
		notes := VerifyInstall(context.Background(), client, "ambassador", values, ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "my-injector service has ready endpoints")
	})
}

func TestVerifyInstall_NothingEnabled(t *testing.T) {
	client := fake.NewClientset()
	notes := VerifyInstall(context.Background(), client, "ambassador", map[string]any{
		"quicTunnel":    map[string]any{"enabled": false},
		"agentInjector": map[string]any{"enabled": false},
	}, ClientAuthFacts{})
	assert.Empty(t, notes)
}

func x509ValuesEnabled() map[string]any {
	return map[string]any{
		"agentInjector": map[string]any{"enabled": false},
		"security":      map[string]any{"authentication": map[string]any{"mode": "enforcing"}},
	}
}

func x509ValuesDisabled() map[string]any {
	return map[string]any{
		"agentInjector": map[string]any{"enabled": false},
		"security": map[string]any{"authentication": map[string]any{
			"mode": "enforcing",
			"x509": map[string]any{"enabled": false},
		}},
	}
}

func TestVerifyInstall_X509ClientAuth(t *testing.T) {
	client := fake.NewClientset()

	t.Run("bearer-capable client", func(t *testing.T) {
		notes := VerifyInstall(context.Background(), client, "ambassador", x509ValuesEnabled(), ClientAuthFacts{Bearer: true, X509: true})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "bearer token")
	})
	t.Run("cert-only client", func(t *testing.T) {
		notes := VerifyInstall(context.Background(), client, "ambassador", x509ValuesEnabled(), ClientAuthFacts{X509: true})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "client certificate")
		assert.Contains(t, notes[0].Text, "x509 listener")
	})
	t.Run("client with no usable credentials", func(t *testing.T) {
		notes := VerifyInstall(context.Background(), client, "ambassador", x509ValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "neither a bearer token nor a client certificate")
	})
	t.Run("cert-only client with x509 disabled", func(t *testing.T) {
		notes := VerifyInstall(context.Background(), client, "ambassador", x509ValuesDisabled(), ClientAuthFacts{X509: true})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "x509 client authentication is disabled")
	})
	t.Run("bearer client with x509 disabled", func(t *testing.T) {
		notes := VerifyInstall(context.Background(), client, "ambassador", x509ValuesDisabled(), ClientAuthFacts{Bearer: true})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "bearer token")
	})
	t.Run("authentication not enforced by the values", func(t *testing.T) {
		values := map[string]any{"agentInjector": map[string]any{"enabled": false}}
		notes := VerifyInstall(context.Background(), client, "ambassador", values, ClientAuthFacts{})
		assert.Empty(t, notes)
	})
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

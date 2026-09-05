package setup

import (
	"bytes"
	"context"
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/helm"
)

func quicValuesEnabled() *helm.Values {
	return &helm.Values{
		QuicTunnel:    helm.QuicTunnel{Enabled: new(true)},
		AgentInjector: helm.AgentInjector{Enabled: new(false)},
	}
}

func injectorValuesEnabled() *helm.Values {
	return &helm.Values{AgentInjector: helm.AgentInjector{Enabled: new(true)}}
}

func quicService(svcType core.ServiceType, mod ...func(*core.Service)) *core.Service {
	svc := &core.Service{
		ObjectMeta: meta.ObjectMeta{Name: quicServiceName, Namespace: "ambassador"},
		Spec:       core.ServiceSpec{Type: svcType},
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
		client := fake.NewClientset(quicService(core.ServiceTypeLoadBalancer, func(svc *core.Service) {
			svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{IP: "1.2.3.4"}}
			svc.Spec.Ports = []core.ServicePort{{Name: "quic", Port: 7778}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess, nil)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "QUIC endpoint reachable from this workstation at 1.2.3.4:7778")
	})
	t.Run("hostname ingress, reachable", func(t *testing.T) {
		client := fake.NewClientset(quicService(core.ServiceTypeLoadBalancer, func(svc *core.Service) {
			svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{Hostname: "lb.example.com"}}
			svc.Spec.Ports = []core.ServicePort{{Name: "quic", Port: 7778}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess, nil)
		require.Len(t, notes, 1)
		assert.Contains(t, notes[0].Text, "lb.example.com:7778")
	})
	t.Run("no ingress within the deadline", func(t *testing.T) {
		client := fake.NewClientset(quicService(core.ServiceTypeLoadBalancer))
		notes := VerifyInstall(shortCtx(t), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "no QUIC endpoint yet")
	})
	t.Run("assigned ingress, port not identifiable", func(t *testing.T) {
		client := fake.NewClientset(quicService(core.ServiceTypeLoadBalancer, func(svc *core.Service) {
			svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{IP: "1.2.3.4"}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess, nil)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "dial address could not be determined")
	})
	t.Run("assigned ingress, not reachable", func(t *testing.T) {
		client := fake.NewClientset(quicService(core.ServiceTypeLoadBalancer, func(svc *core.Service) {
			svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{IP: "1.2.3.4"}}
			svc.Spec.Ports = []core.ServicePort{{Name: "quic", Port: 7778}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{},
			func(ctx context.Context, addr string, tlsConf *tls.Config) error { return context.DeadlineExceeded }, nil)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "not reachable over UDP")
	})
}

func TestVerifyInstall_QuicNodePort(t *testing.T) {
	t.Run("allocated node port, reachable", func(t *testing.T) {
		client := fake.NewClientset(
			quicService(core.ServiceTypeNodePort, func(svc *core.Service) {
				svc.Spec.Ports = []core.ServicePort{{Name: "quic", Port: 7778, NodePort: 31234}}
			}),
			&core.Node{
				ObjectMeta: meta.ObjectMeta{Name: "node1"},
				Status: core.NodeStatus{
					Addresses: []core.NodeAddress{{Type: core.NodeInternalIP, Address: "172.18.0.2"}},
				},
			},
		)
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess, nil)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "reachable from this workstation at 172.18.0.2:31234")
	})
	t.Run("allocated node port, no node address", func(t *testing.T) {
		client := fake.NewClientset(quicService(core.ServiceTypeNodePort, func(svc *core.Service) {
			svc.Spec.Ports = []core.ServicePort{{Name: "quic", Port: 7778, NodePort: 31234}}
		}))
		notes := verifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{}, fakeDialSuccess, nil)
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "dial address could not be determined")
	})
	t.Run("no node port allocated", func(t *testing.T) {
		client := fake.NewClientset(quicService(core.ServiceTypeNodePort, func(svc *core.Service) {
			svc.Spec.Ports = []core.ServicePort{{Name: "quic", Port: 7778}}
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
		client := fake.NewClientset(quicService(core.ServiceTypeClusterIP))
		notes := VerifyInstall(context.Background(), client, "ambassador", quicValuesEnabled(), ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteWarning, notes[0].Level)
		assert.Contains(t, notes[0].Text, "ClusterIP")
	})
}

func injectorSlice(ready bool) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: meta.ObjectMeta{
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
		notes := VerifyInstall(context.Background(), client, "ambassador", &helm.Values{}, ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "ready endpoints")
	})
	t.Run("custom injector name is honored", func(t *testing.T) {
		slice := injectorSlice(true)
		slice.Labels[discoveryv1.LabelServiceName] = "my-injector"
		client := fake.NewClientset(slice)
		values := &helm.Values{AgentInjector: helm.AgentInjector{Name: new("my-injector")}}
		notes := VerifyInstall(context.Background(), client, "ambassador", values, ClientAuthFacts{})
		require.Len(t, notes, 1)
		assert.Equal(t, NoteInfo, notes[0].Level)
		assert.Contains(t, notes[0].Text, "my-injector service has ready endpoints")
	})
}

func TestVerifyInstall_NothingEnabled(t *testing.T) {
	client := fake.NewClientset()
	notes := VerifyInstall(context.Background(), client, "ambassador", &helm.Values{
		QuicTunnel:    helm.QuicTunnel{Enabled: new(false)},
		AgentInjector: helm.AgentInjector{Enabled: new(false)},
	}, ClientAuthFacts{})
	assert.Empty(t, notes)
}

func x509ValuesEnabled() *helm.Values {
	return &helm.Values{
		AgentInjector: helm.AgentInjector{Enabled: new(false)},
		Security:      helm.Security{Authentication: helm.Authentication{Mode: new(helm.AuthModeEnforcing)}},
	}
}

func x509ValuesDisabled() *helm.Values {
	return &helm.Values{
		AgentInjector: helm.AgentInjector{Enabled: new(false)},
		Security: helm.Security{Authentication: helm.Authentication{
			Mode: new(helm.AuthModeEnforcing),
			X509: helm.X509{Enabled: new(false)},
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
		values := &helm.Values{AgentInjector: helm.AgentInjector{Enabled: new(false)}}
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

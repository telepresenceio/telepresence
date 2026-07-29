package setup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

// -- classification against a fake dialer, no sockets involved --

func TestQuicReachabilityFinding_Success(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return nil
	}, "1.2.3.4:7778")
	assert.Equal(t, VerdictYes, f.Verdict)
	assert.Contains(t, f.Evidence[0], "QUIC endpoint reachable from this workstation at 1.2.3.4:7778")
}

func TestQuicReachabilityFinding_PeerResponded(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return &quic.TransportError{Remote: true, ErrorCode: 0x178, ErrorMessage: "tls: no application protocol"}
	}, "1.2.3.4:7778")
	assert.Equal(t, VerdictYes, f.Verdict)
	assert.Contains(t, f.Evidence[0], "reachable from this workstation")
}

func TestQuicReachabilityFinding_Timeout(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return context.DeadlineExceeded
	}, "1.2.3.4:7778")
	assert.Equal(t, VerdictNo, f.Verdict)
	assert.Contains(t, f.Evidence[0], "not reachable over UDP")
	assert.Contains(t, f.Evidence[0], "1.2.3.4:7778")
}

func TestQuicReachabilityFinding_DialSetupError(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return &net.DNSError{Err: "no such host", Name: "quic.example.invalid", IsNotFound: true}
	}, "quic.example.invalid:7778")
	assert.Equal(t, VerdictUnknown, f.Verdict)
	assert.Contains(t, f.Evidence[0], "could not attempt a QUIC dial")
}

func TestIsQuicPeerResponse(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"transport error", &quic.TransportError{Remote: true, ErrorCode: 1}, true},
		{"application error", &quic.ApplicationError{Remote: true, ErrorCode: 1}, true},
		{"version negotiation error", &quic.VersionNegotiationError{}, true},
		{"stateless reset error", &quic.StatelessResetError{}, true},
		{"plain error", errors.New("boom"), false},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"dns error", &net.DNSError{Err: "no such host"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, isQuicPeerResponse(c.err))
		})
	}
}

// -- dial address resolution --

func TestQuicDialAddr_LoadBalancer(t *testing.T) {
	t.Run("named quic port", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Name: "health", Port: 8080}, {Name: "quic", Port: 7778}},
		}}
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
		addr, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3.4:7778", addr)
	})
	t.Run("hostname ingress", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Name: "quic", Port: 7778}},
		}}
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: "lb.example.com"}}
		addr, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "lb.example.com:7778", addr)
	})
	t.Run("sole unnamed port", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Port: 7778}},
		}}
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
		addr, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3.4:7778", addr)
	})
	t.Run("no identifiable port", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Name: "a", Port: 1}, {Name: "b", Port: 2}},
		}}
		svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "1.2.3.4"}}
		_, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		assert.Error(t, err)
	})
	t.Run("no ingress assigned", func(t *testing.T) {
		svc := &corev1.Service{Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{{Name: "quic", Port: 7778}},
		}}
		_, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		assert.Error(t, err)
	})
}

func TestQuicDialAddr_NodePort(t *testing.T) {
	svc := &corev1.Service{Spec: corev1.ServiceSpec{
		Type:  corev1.ServiceTypeNodePort,
		Ports: []corev1.ServicePort{{Name: "quic", Port: 7778, NodePort: 31234}},
	}}
	t.Run("prefers external over internal", func(t *testing.T) {
		client := fake.NewClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.0.1"},
				{Type: corev1.NodeExternalIP, Address: "203.0.113.1"},
			}},
		})
		addr, err := quicDialAddr(context.Background(), client, svc)
		require.NoError(t, err)
		assert.Equal(t, "203.0.113.1:31234", addr)
	})
	t.Run("falls back to internal", func(t *testing.T) {
		client := fake.NewClientset(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: corev1.NodeStatus{Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "172.18.0.2"},
			}},
		})
		addr, err := quicDialAddr(context.Background(), client, svc)
		require.NoError(t, err)
		assert.Equal(t, "172.18.0.2:31234", addr)
	})
	t.Run("no node has a usable address", func(t *testing.T) {
		client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}})
		_, err := quicDialAddr(context.Background(), client, svc)
		assert.Error(t, err)
	})
	t.Run("node list denied", func(t *testing.T) {
		client := fake.NewClientset()
		client.PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("denied"))
		})
		_, err := quicDialAddr(context.Background(), client, svc)
		assert.Error(t, err)
	})
	t.Run("no allocated node port", func(t *testing.T) {
		unallocated := &corev1.Service{Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeNodePort,
			Ports: []corev1.ServicePort{{Name: "quic", Port: 7778}},
		}}
		_, err := quicDialAddr(context.Background(), fake.NewClientset(), unallocated)
		assert.Error(t, err)
	})
}

func TestQuicDialAddr_UnsupportedType(t *testing.T) {
	svc := &corev1.Service{Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP}}
	_, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
	assert.Error(t, err)
}

// -- against a real local quic-go listener: no fakes on either side of the dial --

func genSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "telepresence-test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

// listenQuic starts a real QUIC listener on loopback offering alpn, accepting at most one
// connection in the background so the dial under test has a live peer.
func listenQuic(t *testing.T, alpn string) string {
	t.Helper()
	cert := genSelfSignedCert(t)
	ln, err := quic.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{alpn},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept(context.Background())
		if err == nil {
			_ = conn.CloseWithError(0, "")
		}
	}()
	return ln.Addr().String()
}

func TestQuicGoDial_RealListenerAccepts(t *testing.T) {
	addr := listenQuic(t, "tp-tunnel")
	f := quicReachabilityFinding(context.Background(), quicGoDial, addr)
	assert.Equal(t, VerdictYes, f.Verdict)
	assert.Contains(t, f.Evidence[0], "reachable from this workstation at "+addr)
}

func TestQuicGoDial_RealListenerRejectsALPN(t *testing.T) {
	addr := listenQuic(t, "some-other-protocol")
	f := quicReachabilityFinding(context.Background(), quicGoDial, addr)
	assert.Equal(t, VerdictYes, f.Verdict, "a protocol-level rejection still proves the peer answered")
}

func TestQuicGoDial_NothingListening(t *testing.T) {
	// Bind an ephemeral UDP port and release it immediately so the address is very
	// likely free, but nothing is listening on it for the dial that follows.
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	addr := c.LocalAddr().String()
	require.NoError(t, c.Close())

	f := quicReachabilityFinding(context.Background(), quicGoDial, addr)
	assert.Equal(t, VerdictNo, f.Verdict)
	assert.Contains(t, f.Evidence[0], "not reachable over UDP")
}

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
	"net/netip"
	"syscall" //nolint:depguard // "unix" don't work on windows
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

// -- classification against a fake dialer, no sockets involved --

// fakeDiagnoseNone is a quicDiagnoser stand-in that learns nothing.
func fakeDiagnoseNone(context.Context, string, core.ServiceType) string { return "" }

// fakeDiagnoseSentence is a quicDiagnoser stand-in that always produces sentence.
func fakeDiagnoseSentence(sentence string) quicDiagnoser {
	return func(context.Context, string, core.ServiceType) string { return sentence }
}

func TestQuicReachabilityFinding_Success(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return nil
	}, fakeDiagnoseNone, "1.2.3.4:7778", core.ServiceTypeLoadBalancer)
	assert.Equal(t, VerdictYes, f.Verdict)
	assert.Contains(t, f.Evidence[0], "QUIC endpoint reachable from this workstation at 1.2.3.4:7778")
}

func TestQuicReachabilityFinding_PeerResponded(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return &quic.TransportError{Remote: true, ErrorCode: 0x178, ErrorMessage: "tls: no application protocol"}
	}, fakeDiagnoseNone, "1.2.3.4:7778", core.ServiceTypeLoadBalancer)
	assert.Equal(t, VerdictYes, f.Verdict)
	assert.Contains(t, f.Evidence[0], "reachable from this workstation")
}

func TestQuicReachabilityFinding_ConnRefused(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return &net.OpError{Op: "read", Net: "udp", Err: syscall.ECONNREFUSED}
	}, fakeDiagnoseNone, "1.2.3.4:7778", core.ServiceTypeLoadBalancer)
	assert.Equal(t, VerdictNo, f.Verdict)
	assert.Contains(t, f.Evidence[0], "not reachable over UDP")
	assert.Contains(t, f.Evidence[0], "nothing listening on UDP port 7778")
}

func TestQuicReachabilityFinding_Timeout(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return context.DeadlineExceeded
	}, fakeDiagnoseNone, "1.2.3.4:7778", core.ServiceTypeLoadBalancer)
	assert.Equal(t, VerdictNo, f.Verdict)
	assert.Contains(t, f.Evidence[0], "not reachable over UDP")
	assert.Contains(t, f.Evidence[0], "1.2.3.4:7778")
	assert.NotContains(t, f.Evidence[0], "firewall")
}

func TestQuicReachabilityFinding_TimeoutWithDiagnosis(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return context.DeadlineExceeded
	}, fakeDiagnoseSentence("The host does not answer on TCP either, so it is unreachable from this workstation."), "1.2.3.4:7778", core.ServiceTypeLoadBalancer)
	assert.Equal(t, VerdictNo, f.Verdict)
	assert.Contains(t, f.Evidence[0], "not reachable over UDP")
	assert.Contains(t, f.Evidence[0], "The host does not answer on TCP either")
}

func TestQuicReachabilityFinding_DialSetupError(t *testing.T) {
	f := quicReachabilityFinding(context.Background(), func(context.Context, string, *tls.Config) error {
		return &net.DNSError{Err: "no such host", Name: "quic.example.invalid", IsNotFound: true}
	}, fakeDiagnoseNone, "quic.example.invalid:7778", core.ServiceTypeLoadBalancer)
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
		svc := &core.Service{Spec: core.ServiceSpec{
			Type:  core.ServiceTypeLoadBalancer,
			Ports: []core.ServicePort{{Name: "health", Port: 8080}, {Name: "quic", Port: 7778}},
		}}
		svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{IP: "1.2.3.4"}}
		addr, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3.4:7778", addr)
	})
	t.Run("hostname ingress", func(t *testing.T) {
		svc := &core.Service{Spec: core.ServiceSpec{
			Type:  core.ServiceTypeLoadBalancer,
			Ports: []core.ServicePort{{Name: "quic", Port: 7778}},
		}}
		svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{Hostname: "lb.example.com"}}
		addr, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "lb.example.com:7778", addr)
	})
	t.Run("sole unnamed port", func(t *testing.T) {
		svc := &core.Service{Spec: core.ServiceSpec{
			Type:  core.ServiceTypeLoadBalancer,
			Ports: []core.ServicePort{{Port: 7778}},
		}}
		svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{IP: "1.2.3.4"}}
		addr, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		require.NoError(t, err)
		assert.Equal(t, "1.2.3.4:7778", addr)
	})
	t.Run("no identifiable port", func(t *testing.T) {
		svc := &core.Service{Spec: core.ServiceSpec{
			Type:  core.ServiceTypeLoadBalancer,
			Ports: []core.ServicePort{{Name: "a", Port: 1}, {Name: "b", Port: 2}},
		}}
		svc.Status.LoadBalancer.Ingress = []core.LoadBalancerIngress{{IP: "1.2.3.4"}}
		_, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		assert.Error(t, err)
	})
	t.Run("no ingress assigned", func(t *testing.T) {
		svc := &core.Service{Spec: core.ServiceSpec{
			Type:  core.ServiceTypeLoadBalancer,
			Ports: []core.ServicePort{{Name: "quic", Port: 7778}},
		}}
		_, err := quicDialAddr(context.Background(), fake.NewClientset(), svc)
		assert.Error(t, err)
	})
}

func TestQuicDialAddr_NodePort(t *testing.T) {
	svc := &core.Service{Spec: core.ServiceSpec{
		Type:  core.ServiceTypeNodePort,
		Ports: []core.ServicePort{{Name: "quic", Port: 7778, NodePort: 31234}},
	}}
	t.Run("prefers external over internal", func(t *testing.T) {
		client := fake.NewClientset(&core.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: core.NodeStatus{Addresses: []core.NodeAddress{
				{Type: core.NodeInternalIP, Address: "10.0.0.1"},
				{Type: core.NodeExternalIP, Address: "203.0.113.1"},
			}},
		})
		addr, err := quicDialAddr(context.Background(), client, svc)
		require.NoError(t, err)
		assert.Equal(t, "203.0.113.1:31234", addr)
	})
	t.Run("falls back to internal", func(t *testing.T) {
		client := fake.NewClientset(&core.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "n1"},
			Status: core.NodeStatus{Addresses: []core.NodeAddress{
				{Type: core.NodeInternalIP, Address: "172.18.0.2"},
			}},
		})
		addr, err := quicDialAddr(context.Background(), client, svc)
		require.NoError(t, err)
		assert.Equal(t, "172.18.0.2:31234", addr)
	})
	t.Run("no node has a usable address", func(t *testing.T) {
		client := fake.NewClientset(&core.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}})
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
		unallocated := &core.Service{Spec: core.ServiceSpec{
			Type:  core.ServiceTypeNodePort,
			Ports: []core.ServicePort{{Name: "quic", Port: 7778}},
		}}
		_, err := quicDialAddr(context.Background(), fake.NewClientset(), unallocated)
		assert.Error(t, err)
	})
}

func TestQuicDialAddr_UnsupportedType(t *testing.T) {
	svc := &core.Service{Spec: core.ServiceSpec{Type: core.ServiceTypeClusterIP}}
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
	f := quicReachabilityFinding(context.Background(), quicGoDial, fakeDiagnoseNone, addr, core.ServiceTypeLoadBalancer)
	assert.Equal(t, VerdictYes, f.Verdict)
	assert.Contains(t, f.Evidence[0], "reachable from this workstation at "+addr)
}

func TestQuicGoDial_RealListenerRejectsALPN(t *testing.T) {
	addr := listenQuic(t, "some-other-protocol")
	f := quicReachabilityFinding(context.Background(), quicGoDial, fakeDiagnoseNone, addr, core.ServiceTypeLoadBalancer)
	assert.Equal(t, VerdictYes, f.Verdict, "a protocol-level rejection still proves the peer answered")
}

func TestQuicGoDial_NothingListening(t *testing.T) {
	// Bind an ephemeral UDP port and release it immediately so the address is very
	// likely free, but nothing is listening on it for the dial that follows.
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	addr := c.LocalAddr().String()
	require.NoError(t, c.Close())

	f := quicReachabilityFinding(context.Background(), quicGoDial, fakeDiagnoseNone, addr, core.ServiceTypeLoadBalancer)
	assert.Equal(t, VerdictNo, f.Verdict)
	assert.Contains(t, f.Evidence[0], "not reachable over UDP")
}

// -- diagnoseQuicSilence: a real TCP dial, no fakes --

func TestDiagnoseQuicSilence_TCPRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	sentence := diagnoseQuicSilence(context.Background(), addr, core.ServiceTypeLoadBalancer)
	assert.Contains(t, sentence, "answers on TCP but not on UDP")
	assert.Contains(t, sentence, "LoadBalancer's UDP forwarding")
}

func TestDiagnoseQuicSilence_UnparsableAddr(t *testing.T) {
	sentence := diagnoseQuicSilence(context.Background(), "not-a-host-port", core.ServiceTypeLoadBalancer)
	assert.Empty(t, sentence)
}

// -- routeClauseForDest: the longest-prefix selection, no OS routing table involved --

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	require.NoError(t, err)
	return p
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	require.NoError(t, err)
	return a
}

func TestRouteClauseForDest(t *testing.T) {
	dst := mustAddr(t, "172.18.0.2")

	t.Run("no routes", func(t *testing.T) {
		assert.Empty(t, routeClauseForDest(nil, dst))
	})

	t.Run("longest matching non-default prefix wins", func(t *testing.T) {
		table := []*routing.Route{
			{InterfaceName: "eth0", RoutedNet: mustPrefix(t, "172.0.0.0/8")},
			{InterfaceName: "docker0", RoutedNet: mustPrefix(t, "172.18.0.0/16")},
			{InterfaceName: "wlan0", Default: true, RoutedNet: mustPrefix(t, "0.0.0.0/0"), Gateway: mustAddr(t, "192.168.1.1")},
		}
		clause := routeClauseForDest(table, dst)
		assert.Contains(t, clause, "interface docker0")
		assert.Contains(t, clause, "Docker bridge")
	})

	t.Run("br- prefix is also flagged as a Docker bridge", func(t *testing.T) {
		table := []*routing.Route{
			{InterfaceName: "br-abcdef123456", RoutedNet: mustPrefix(t, "172.18.0.0/16")},
		}
		clause := routeClauseForDest(table, dst)
		assert.Contains(t, clause, "interface br-abcdef123456")
		assert.Contains(t, clause, "Docker bridge")
	})

	t.Run("non-bridge match carries no parenthetical", func(t *testing.T) {
		table := []*routing.Route{
			{InterfaceName: "eth0", RoutedNet: mustPrefix(t, "172.18.0.0/16")},
		}
		clause := routeClauseForDest(table, dst)
		assert.Equal(t, ", which routes it via interface eth0", clause)
	})

	t.Run("falls back to the default route when nothing more specific matches", func(t *testing.T) {
		table := []*routing.Route{
			{InterfaceName: "eth1", RoutedNet: mustPrefix(t, "10.0.0.0/8")},
			{InterfaceName: "wlan0", Default: true, RoutedNet: mustPrefix(t, "0.0.0.0/0"), Gateway: mustAddr(t, "192.168.1.1")},
		}
		clause := routeClauseForDest(table, dst)
		assert.Equal(t, ", which routes it via interface wlan0 via the default gateway 192.168.1.1", clause)
	})

	t.Run("default route without a gateway carries no gateway clause", func(t *testing.T) {
		table := []*routing.Route{
			{InterfaceName: "wlan0", Default: true, RoutedNet: mustPrefix(t, "0.0.0.0/0")},
		}
		clause := routeClauseForDest(table, dst)
		assert.Equal(t, ", which routes it via interface wlan0", clause)
	})
}

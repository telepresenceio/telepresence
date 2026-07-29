package setup

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

func podCIDRNode(cidrs ...string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-" + cidrs[0]},
		Spec:       corev1.NodeSpec{PodCIDR: cidrs[0], PodCIDRs: cidrs},
	}
}

func localRoute(dst, ifName string) *routing.Route {
	return &routing.Route{RoutedNet: netip.MustParsePrefix(dst), InterfaceName: ifName}
}

func defaultRoute(dst, ifName string) *routing.Route {
	r := localRoute(dst, ifName)
	r.Default = true
	return r
}

func routeProber(routes []*routing.Route, err error) *Prober {
	return &Prober{
		KubeClient:       fake.NewClientset(),
		ManagerNamespace: "ambassador",
		RouteSource: func(context.Context) ([]*routing.Route, error) {
			return routes, err
		},
	}
}

func TestProbeRouting(t *testing.T) {
	nodes := []corev1.Node{podCIDRNode("10.244.0.0/24"), podCIDRNode("10.244.1.0/24")}

	tests := []struct {
		name          string
		nodes         []corev1.Node
		routes        []*routing.Route
		routeErr      error
		wantVerdict   Verdict
		wantConflicts int
		wantEvidence  string
	}{
		{
			name:          "v4 pod CIDR overlaps a VPN-style route",
			nodes:         nodes,
			routes:        []*routing.Route{localRoute("10.0.0.0/8", "tun0")},
			wantVerdict:   VerdictNo,
			wantConflicts: 2,
			wantEvidence:  "10.244.0.0/24 (pod CIDR) overlaps local route 10.0.0.0/8 dev tun0",
		},
		{
			name:          "host route inside a pod CIDR",
			nodes:         nodes,
			routes:        []*routing.Route{localRoute("10.244.0.17/32", "eth1")},
			wantVerdict:   VerdictNo,
			wantConflicts: 1,
			wantEvidence:  "10.244.0.0/24 (pod CIDR) overlaps local route 10.244.0.17/32 dev eth1",
		},
		{
			name:          "v6 pod CIDR overlap",
			nodes:         []corev1.Node{podCIDRNode("fd00:10:244::/64")},
			routes:        []*routing.Route{localRoute("fd00:10::/32", "wg0")},
			wantVerdict:   VerdictNo,
			wantConflicts: 1,
			wantEvidence:  "fd00:10:244::/64 (pod CIDR) overlaps local route fd00:10::/32 dev wg0",
		},
		{
			name:        "no overlap",
			nodes:       nodes,
			routes:      []*routing.Route{localRoute("192.168.1.0/24", "eth0"), localRoute("172.17.0.0/16", "docker0")},
			wantVerdict: VerdictYes,
		},
		{
			name:        "default route is excluded",
			nodes:       nodes,
			routes:      []*routing.Route{defaultRoute("0.0.0.0/0", "eth0")},
			wantVerdict: VerdictYes,
		},
		{
			name:        "zero-bits route is excluded even without the default flag",
			nodes:       nodes,
			routes:      []*routing.Route{localRoute("0.0.0.0/0", "eth0")},
			wantVerdict: VerdictYes,
		},
		{
			name:        "loopback and link-local destinations are excluded",
			nodes:       []corev1.Node{podCIDRNode("127.0.0.0/16"), podCIDRNode("fe80::/64")},
			routes:      []*routing.Route{localRoute("127.0.0.0/8", "lo"), localRoute("fe80::/64", "eth0")},
			wantVerdict: VerdictYes,
		},
		{
			name:        "unreadable routing table",
			nodes:       nodes,
			routeErr:    errors.New("permission denied"),
			wantVerdict: VerdictUnknown,
		},
		{
			name:        "no cluster subnets",
			nodes:       nil,
			routes:      []*routing.Route{localRoute("10.0.0.0/8", "tun0")},
			wantVerdict: VerdictUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := routeProber(tt.routes, tt.routeErr)
			facts := p.probeRouting(context.Background(), tt.nodes)
			assert.Equal(t, tt.wantVerdict, facts.Summary.Verdict)
			assert.Len(t, facts.Conflicts, tt.wantConflicts)
			if tt.wantEvidence != "" {
				assert.Contains(t, facts.Summary.Evidence, tt.wantEvidence)
			}
		})
	}
}

func TestProbeRouting_EstimatedServiceCIDR(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "default"},
			Spec:       corev1.ServiceSpec{ClusterIP: "10.96.0.10"},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "headless", Namespace: "default"},
			Spec:       corev1.ServiceSpec{ClusterIP: "None"},
		},
	)
	p := &Prober{
		KubeClient:       client,
		ManagerNamespace: "ambassador",
		RouteSource: func(context.Context) ([]*routing.Route, error) {
			return []*routing.Route{localRoute("10.96.0.0/12", "tun0")}, nil
		},
	}
	facts := p.probeRouting(context.Background(), nil)
	require.Equal(t, VerdictNo, facts.Summary.Verdict)
	require.Len(t, facts.Conflicts, 1)
	assert.Equal(t, "10.96.0.0/16", facts.Conflicts[0].ClusterSubnet)
	assert.Equal(t, sourceServiceCIDR, facts.Conflicts[0].Source)
	assert.Contains(t, facts.Summary.Evidence[0], "service CIDR (estimated)")
}

func TestRoutingFacts_ConflictingSubnets(t *testing.T) {
	r := &RoutingFacts{Conflicts: []RoutingConflict{
		{ClusterSubnet: "10.244.0.0/16"},
		{ClusterSubnet: "10.96.0.0/16"},
		{ClusterSubnet: "10.244.0.0/16"},
	}}
	assert.Equal(t, []string{"10.244.0.0/16", "10.96.0.0/16"}, r.ConflictingSubnets())
}

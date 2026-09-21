package setup

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/telepresenceio/telepresence/v2/pkg/routing"
)

func podCIDRNode(cidrs ...string) core.Node {
	return core.Node{
		ObjectMeta: meta.ObjectMeta{Name: "node-" + cidrs[0]},
		Spec:       core.NodeSpec{PodCIDR: cidrs[0], PodCIDRs: cidrs},
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
		// Deterministic: no active session, ever, unless a test overrides it.
		ActiveRoutes: func(context.Context) ([]netip.Prefix, bool) {
			return nil, false
		},
	}
}

func TestProbeRouting(t *testing.T) {
	nodes := []core.Node{podCIDRNode("10.244.0.0/24"), podCIDRNode("10.244.1.0/24")}

	tests := []struct {
		name          string
		nodes         []core.Node
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
			nodes:         []core.Node{podCIDRNode("fd00:10:244::/64")},
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
			nodes:       []core.Node{podCIDRNode("127.0.0.0/16"), podCIDRNode("fe80::/64")},
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
			facts := p.probeRouting(context.Background(), tt.nodes, nil)
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
		&core.Service{
			ObjectMeta: meta.ObjectMeta{Name: "svc", Namespace: "default"},
			Spec:       core.ServiceSpec{ClusterIP: "10.96.0.10"},
		},
		&core.Service{
			ObjectMeta: meta.ObjectMeta{Name: "headless", Namespace: "default"},
			Spec:       core.ServiceSpec{ClusterIP: "None"},
		},
	)
	p := &Prober{
		KubeClient:       client,
		ManagerNamespace: "ambassador",
		RouteSource: func(context.Context) ([]*routing.Route, error) {
			return []*routing.Route{localRoute("10.96.0.0/12", "tun0")}, nil
		},
		ActiveRoutes: func(context.Context) ([]netip.Prefix, bool) {
			return nil, false
		},
	}
	services, _ := p.listServices(context.Background())
	facts := p.probeRouting(context.Background(), nil, services)
	require.Equal(t, VerdictNo, facts.Summary.Verdict)
	require.Len(t, facts.Conflicts, 1)
	assert.Equal(t, "10.96.0.0/16", facts.Conflicts[0].ClusterSubnet)
	assert.Equal(t, sourceServiceCIDR, facts.Conflicts[0].Source)
	assert.Contains(t, facts.Summary.Evidence[0], "service CIDR (estimated)")
}

func TestProbeRouting_ActiveSessionRoutesIgnored(t *testing.T) {
	nodes := []core.Node{podCIDRNode("10.244.0.0/24")}

	t.Run("route on the tel device is ignored", func(t *testing.T) {
		p := routeProber([]*routing.Route{localRoute("10.244.0.0/24", "tel0")}, nil)
		facts := p.probeRouting(context.Background(), nodes, nil)
		assert.Equal(t, VerdictYes, facts.Summary.Verdict)
		assert.Empty(t, facts.Conflicts)
		assert.Equal(t, []string{"10.244.0.0/24"}, facts.ActiveSessionSubnets)
		assert.Contains(t, facts.Summary.Evidence, exclusionEvidence([]string{"10.244.0.0/24"}))
	})

	t.Run("plain interface route is ignored when ActiveRoutes reports it", func(t *testing.T) {
		p := routeProber([]*routing.Route{localRoute("10.244.0.0/24", "eth0")}, nil)
		p.ActiveRoutes = func(context.Context) ([]netip.Prefix, bool) {
			return []netip.Prefix{netip.MustParsePrefix("10.244.0.0/24")}, true
		}
		facts := p.probeRouting(context.Background(), nodes, nil)
		assert.Equal(t, VerdictYes, facts.Summary.Verdict)
		assert.Empty(t, facts.Conflicts)
		assert.Equal(t, []string{"10.244.0.0/24"}, facts.ActiveSessionSubnets)
	})

	t.Run("the same route is reported when ActiveRoutes has no session", func(t *testing.T) {
		p := routeProber([]*routing.Route{localRoute("10.244.0.0/24", "eth0")}, nil)
		p.ActiveRoutes = func(context.Context) ([]netip.Prefix, bool) {
			return []netip.Prefix{netip.MustParsePrefix("10.244.0.0/24")}, false
		}
		facts := p.probeRouting(context.Background(), nodes, nil)
		assert.Equal(t, VerdictNo, facts.Summary.Verdict)
		assert.Len(t, facts.Conflicts, 1)
		assert.Empty(t, facts.ActiveSessionSubnets)
	})

	t.Run("activeSessionSubnets is carried in the JSON facts", func(t *testing.T) {
		p := routeProber([]*routing.Route{localRoute("10.244.0.0/24", "tel0")}, nil)
		facts := p.probeRouting(context.Background(), nodes, nil)
		data, err := json.Marshal(facts)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"activeSessionSubnets":["10.244.0.0/24"]`)
	})
}

func TestRoutingFacts_ConflictingSubnets(t *testing.T) {
	r := &RoutingFacts{Conflicts: []RoutingConflict{
		{ClusterSubnet: "10.244.0.0/16"},
		{ClusterSubnet: "10.96.0.0/16"},
		{ClusterSubnet: "10.244.0.0/16"},
	}}
	assert.Equal(t, []string{"10.244.0.0/16", "10.96.0.0/16"}, r.ConflictingSubnets())
}

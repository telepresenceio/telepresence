//go:build linux

package agentnft

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// TestConfigForFlattensIntercepts verifies the sidecar -> Config translation:
// every container's port-unique intercepts are flattened into one list,
// numeric target ports record the proxy port, symbolic ones don't, and the
// mesh-dial subnets are carried over unchanged.
func TestConfigForFlattensIntercepts(t *testing.T) {
	podIP := netip.MustParseAddr("10.129.70.53")
	owner := OwnerMatch{UseGID: true, ID: 7439}
	meshSubnets := []netip.Prefix{netip.MustParsePrefix("240.240.0.0/16")}
	sc := &agentconfig.Sidecar{
		MeshDialSubnets: meshSubnets,
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{Protocol: types.ProtoTCP, ContainerPort: 8000, AgentPort: 9900, TargetPortNumeric: true},
					{Protocol: types.ProtoTCP, ContainerPort: 8001, AgentPort: 9901}, // symbolic
					{Protocol: types.ProtoUDP, ContainerPort: 8081, AgentPort: 9081},
				},
			},
			{
				Name: "sidecar",
				Intercepts: []*agentconfig.Intercept{
					{Protocol: types.ProtoTCP, ContainerPort: 9000, AgentPort: 9950, TargetPortNumeric: true},
				},
			},
		},
	}

	cfg := ConfigFor(sc, "lo", podIP, owner)

	require.Equal(t, podIP, cfg.PodIP)
	require.Equal(t, "lo", cfg.Loopback)
	require.Equal(t, owner, cfg.Owner)
	require.Equal(t, meshSubnets, cfg.MeshDialSubnets)
	require.Len(t, cfg.Intercepts, 4)

	byCPort := map[uint16]Intercept{}
	for _, ic := range cfg.Intercepts {
		byCPort[ic.ContainerPort] = ic
	}

	numeric := byCPort[8000]
	require.Equal(t, sc.ProxyPort(9900), numeric.ProxyPort, "numeric target must record the proxy port")
	require.NotZero(t, numeric.ProxyPort)

	symbolic := byCPort[8001]
	require.Zero(t, symbolic.ProxyPort, "symbolic target must not record a proxy port")

	udp := byCPort[8081]
	require.Equal(t, types.ProtoUDP, udp.Protocol)
}

// TestConfigForProducesApplicableRuleset ties the translation to Build: the
// resulting Config must construct a valid ruleset.
func TestConfigForProducesApplicableRuleset(t *testing.T) {
	podIP := netip.MustParseAddr("10.129.70.53")
	owner := OwnerMatch{UseGID: false, ID: 1000}
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{Protocol: types.ProtoTCP, ContainerPort: 8000, AgentPort: 9900, TargetPortNumeric: true},
				},
			},
		},
	}

	cfg := ConfigFor(sc, "lo", podIP, owner)
	rs, err := Build(cfg)
	require.NoError(t, err)
	require.NotNil(t, rs.Prerouting)
	require.NotNil(t, rs.Output)
	require.NotEmpty(t, rs.Rules)
}

//go:build linux

package agentinit

import (
	"net/netip"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentnft"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestTrafficAgentUIDDefaultsToProcessUID(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "")

	uid, err := trafficAgentUID()
	require.NoError(t, err)
	require.Equal(t, uint32(os.Getuid()), uid)
}

func TestTrafficAgentUIDUsesEnvironment(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "1000")

	uid, err := trafficAgentUID()
	require.NoError(t, err)
	require.Equal(t, uint32(1000), uid)
}

func TestTrafficAgentUIDRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "not-a-uid")

	_, err := trafficAgentUID()
	require.Error(t, err)
}

func TestTrafficAgentOwnerUsesGroupFromEnvironment(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "1000")
	t.Setenv(agentconfig.EnvAgentGID, "7439")

	owner, err := trafficAgentOwner()
	require.NoError(t, err)
	require.Equal(t, agentnft.OwnerMatch{UseGID: true, ID: 7439}, owner)
}

func TestTrafficAgentOwnerFallsBackToUID(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "1000")
	t.Setenv(agentconfig.EnvAgentGID, "")

	owner, err := trafficAgentOwner()
	require.NoError(t, err)
	require.Equal(t, agentnft.OwnerMatch{UseGID: false, ID: 1000}, owner)
}

func TestTrafficAgentOwnerRejectsInvalidGroup(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentGID, "not-a-gid")

	_, err := trafficAgentOwner()
	require.Error(t, err)
}

// TestBuildNftConfigFlattensIntercepts verifies the sidecar -> agentnft.Config
// translation: every container's port-unique intercepts are flattened, the
// owner is derived from AGENT_GID, and mesh-dial subnets are carried over.
func TestBuildNftConfigFlattensIntercepts(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentGID, "7439")
	podIP := netip.MustParseAddr("10.129.70.53")
	cfg := &config{Sidecar: &agentconfig.Sidecar{
		MeshDialSubnets: []netip.Prefix{netip.MustParsePrefix("240.240.0.0/16")},
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{Protocol: types.ProtoTCP, ContainerPort: 8080, AgentPort: 9080},
					{Protocol: types.ProtoUDP, ContainerPort: 8081, AgentPort: 9081},
				},
			},
			{
				Name: "sidecar",
				Intercepts: []*agentconfig.Intercept{
					{Protocol: types.ProtoTCP, ContainerPort: 9000, AgentPort: 9900},
				},
			},
		},
	}}

	nftCfg, err := cfg.buildNftConfig("lo", podIP)
	require.NoError(t, err)
	require.Equal(t, podIP, nftCfg.PodIP)
	require.Equal(t, "lo", nftCfg.Loopback)
	require.Equal(t, agentnft.OwnerMatch{UseGID: true, ID: 7439}, nftCfg.Owner)
	require.Equal(t, cfg.MeshDialSubnets, nftCfg.MeshDialSubnets)
	require.Len(t, nftCfg.Intercepts, 3)
	require.Contains(t, nftCfg.Intercepts, agentnft.Intercept{Protocol: types.ProtoTCP, ContainerPort: 8080, AgentPort: 9080})
	require.Contains(t, nftCfg.Intercepts, agentnft.Intercept{Protocol: types.ProtoUDP, ContainerPort: 8081, AgentPort: 9081})
	require.Contains(t, nftCfg.Intercepts, agentnft.Intercept{Protocol: types.ProtoTCP, ContainerPort: 9000, AgentPort: 9900})
}

// TestBuildNftConfigNumericTargetSetsProxyPort verifies that a numeric target
// port records the proxy port (agentinit's proxy-port DNAT input), while a
// symbolic target port leaves ProxyPort zero.
func TestBuildNftConfigNumericTargetSetsProxyPort(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentGID, "7439")
	podIP := netip.MustParseAddr("10.129.70.53")
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{Protocol: types.ProtoTCP, ContainerPort: 8000, AgentPort: 9900, TargetPortNumeric: true},
					{Protocol: types.ProtoTCP, ContainerPort: 8001, AgentPort: 9901}, // symbolic
				},
			},
		},
	}
	cfg := &config{Sidecar: sc}

	nftCfg, err := cfg.buildNftConfig("lo", podIP)
	require.NoError(t, err)
	require.Len(t, nftCfg.Intercepts, 2)

	byCPort := map[uint16]agentnft.Intercept{}
	for _, ic := range nftCfg.Intercepts {
		byCPort[ic.ContainerPort] = ic
	}
	require.Equal(t, sc.ProxyPort(9900), byCPort[8000].ProxyPort, "numeric target must record the proxy port")
	require.NotZero(t, byCPort[8000].ProxyPort)
	require.Zero(t, byCPort[8001].ProxyPort, "symbolic target must not record a proxy port")
}

// TestBuildNftConfigProducesApplicableRuleset ties the translation to Build:
// the resulting config must construct a valid ruleset (root-free), covering the
// representative cases the old iptables tests exercised.
func TestBuildNftConfigProducesApplicableRuleset(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "1000")
	t.Setenv(agentconfig.EnvAgentGID, "") // exercise the UID fallback
	podIP := netip.MustParseAddr("10.129.70.53")
	cfg := &config{Sidecar: &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{Protocol: types.ProtoTCP, ContainerPort: 8000, AgentPort: 9900, TargetPortNumeric: true},
				},
			},
		},
	}}

	nftCfg, err := cfg.buildNftConfig("lo", podIP)
	require.NoError(t, err)
	require.Equal(t, agentnft.OwnerMatch{UseGID: false, ID: 1000}, nftCfg.Owner)

	rs, err := agentnft.Build(nftCfg)
	require.NoError(t, err)
	require.NotNil(t, rs.Prerouting)
	require.NotNil(t, rs.Output)
	require.NotEmpty(t, rs.Rules)
}

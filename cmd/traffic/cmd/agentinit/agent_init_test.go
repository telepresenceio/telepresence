package agentinit

import (
	"context"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

type iptablesCall struct {
	op       string
	table    string
	chain    string
	position int
	rulespec []string
}

type fakeIPTables struct {
	calls []iptablesCall
}

func (f *fakeIPTables) ClearChain(table, chain string) error {
	f.calls = append(f.calls, iptablesCall{op: "clear", table: table, chain: chain})
	return nil
}

func (f *fakeIPTables) AppendUnique(table, chain string, rulespec ...string) error {
	f.calls = append(f.calls, iptablesCall{op: "append", table: table, chain: chain, rulespec: rulespec})
	return nil
}

func (f *fakeIPTables) Insert(table, chain string, pos int, rulespec ...string) error {
	f.calls = append(f.calls, iptablesCall{op: "insert", table: table, chain: chain, position: pos, rulespec: rulespec})
	return nil
}

func requireCall(t *testing.T, calls []iptablesCall, want iptablesCall) {
	t.Helper()
	for _, call := range calls {
		if call.op == want.op && call.table == want.table && call.chain == want.chain &&
			call.position == want.position && strings.Join(call.rulespec, "\x00") == strings.Join(want.rulespec, "\x00") {
			return
		}
	}
	require.Failf(t, "missing iptables call", "wanted %#v in %#v", want, calls)
}

func TestTrafficAgentUIDDefaultsToProcessUID(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "")

	uid, err := trafficAgentUID()
	require.NoError(t, err)
	require.Equal(t, strconv.Itoa(os.Getuid()), uid)
}

func TestTrafficAgentUIDUsesEnvironment(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "1000")

	uid, err := trafficAgentUID()
	require.NoError(t, err)
	require.Equal(t, "1000", uid)
}

func TestTrafficAgentUIDRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "not-a-uid")

	_, err := trafficAgentUID()
	require.Error(t, err)
}

func TestConfigureIptablesCatchesPodIPOutputTraffic(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "1000")
	podIP := netip.MustParseAddr("10.129.70.53")
	ipt := &fakeIPTables{}
	cfg := &config{Sidecar: &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						Protocol:          types.ProtoTCP,
						ContainerPort:     8000,
						AgentPort:         9900,
						TargetPortNumeric: true,
					},
				},
			},
		},
	}}

	err := cfg.configureIptables(context.Background(), ipt, "lo", netip.MustParsePrefix("127.0.0.1/32"), podIP)
	require.NoError(t, err)

	requireCall(t, ipt.calls, iptablesCall{
		op:    "append",
		table: nat,
		chain: "TEL_OUTPUT_TCP",
		rulespec: []string{
			"-p", "tcp", "--dport", "8000",
			"-j", "REDIRECT", "--to-ports", "9900",
		},
	})
	requireCall(t, ipt.calls, iptablesCall{
		op:    "append",
		table: nat,
		chain: "TEL_OUTPUT_TCP",
		rulespec: []string{
			"-p", "tcp", "-d", "10.129.70.53", "--dport", "9912",
			"-j", "DNAT", "--to-destination", "10.129.70.53:8000",
		},
	})
	requireCall(t, ipt.calls, iptablesCall{
		op:       "insert",
		table:    nat,
		chain:    "OUTPUT",
		position: 1,
		rulespec: []string{
			"-p", "tcp",
			"-d", "10.129.70.53",
			"-m", "owner", "!", "--uid-owner", "1000",
			"-j", "TEL_OUTPUT_TCP",
		},
	})
	requireCall(t, ipt.calls, iptablesCall{
		op:       "insert",
		table:    nat,
		chain:    "OUTPUT",
		position: 1,
		rulespec: []string{
			"-p", "tcp",
			"-d", "10.129.70.53",
			"-m", "owner", "--uid-owner", "1000",
			"-j", "TEL_OUTPUT_TCP",
		},
	})
	// The agent-UID mesh bypass must not match DNS traffic, so that the agent's
	// lookups are subjected to a service mesh's DNS interception when present.
	requireCall(t, ipt.calls, iptablesCall{
		op:       "insert",
		table:    nat,
		chain:    "OUTPUT",
		position: 5,
		rulespec: []string{
			"-p", "tcp",
			"-m", "owner", "--uid-owner", "1000",
			"-m", "tcp", "!", "--dport", "53",
			"-j", "RETURN",
		},
	})
	requireCall(t, ipt.calls, iptablesCall{
		op:       "insert",
		table:    nat,
		chain:    "OUTPUT",
		position: 6,
		rulespec: []string{
			"-p", "udp",
			"-m", "owner", "--uid-owner", "1000",
			"-m", "udp", "!", "--dport", "53",
			"-j", "RETURN",
		},
	})
}

func TestConfigureIptablesMeshDialSubnets(t *testing.T) {
	t.Setenv(agentconfig.EnvAgentUID, "1000")
	podIP := netip.MustParseAddr("10.129.70.53")
	ipt := &fakeIPTables{}
	cfg := &config{Sidecar: &agentconfig.Sidecar{
		MeshDialSubnets: []netip.Prefix{
			netip.MustParsePrefix("240.240.0.0/16"),
			netip.MustParsePrefix("fd00:240::/32"), // wrong family, must be skipped
		},
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						Protocol:      types.ProtoTCP,
						ContainerPort: 8000,
						AgentPort:     9900,
					},
				},
			},
		},
	}}

	err := cfg.configureIptables(context.Background(), ipt, "lo", netip.MustParsePrefix("127.0.0.1/32"), podIP)
	require.NoError(t, err)

	// The configured subnets (of the pod's address family) are exempted from the
	// mesh bypass so connections to them are made through the mesh proxy.
	requireCall(t, ipt.calls, iptablesCall{
		op:    "clear",
		table: nat,
		chain: "TEL_MESH_BYPASS",
	})
	requireCall(t, ipt.calls, iptablesCall{
		op:    "append",
		table: nat,
		chain: "TEL_MESH_BYPASS",
		rulespec: []string{
			"-d", "240.240.0.0/16",
			"-j", "RETURN",
		},
	})
	requireCall(t, ipt.calls, iptablesCall{
		op:    "append",
		table: nat,
		chain: "TEL_MESH_BYPASS",
		rulespec: []string{
			"-j", "ACCEPT",
		},
	})
	for _, call := range ipt.calls {
		if call.chain == "TEL_MESH_BYPASS" && len(call.rulespec) > 1 && call.rulespec[1] == "fd00:240::/32" {
			t.Fatalf("subnet of the wrong address family must not be added: %#v", call)
		}
	}
	// The bypass rules target the exemption chain instead of RETURN.
	requireCall(t, ipt.calls, iptablesCall{
		op:       "insert",
		table:    nat,
		chain:    "OUTPUT",
		position: 5,
		rulespec: []string{
			"-p", "tcp",
			"-m", "owner", "--uid-owner", "1000",
			"-m", "tcp", "!", "--dport", "53",
			"-j", "TEL_MESH_BYPASS",
		},
	})
	requireCall(t, ipt.calls, iptablesCall{
		op:       "insert",
		table:    nat,
		chain:    "OUTPUT",
		position: 6,
		rulespec: []string{
			"-p", "udp",
			"-m", "owner", "--uid-owner", "1000",
			"-m", "udp", "!", "--dport", "53",
			"-j", "TEL_MESH_BYPASS",
		},
	})
}

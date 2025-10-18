package state

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// TestExtractPortsFromSidecarNil tests extractPortsFromSidecar with nil sidecar.
func TestExtractPortsFromSidecarNil(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	ports := extractPortsFromSidecar(ctx, nil)
	assert.Nil(t, ports)
}

// TestExtractPortsFromSidecarNoContainers tests extractPortsFromSidecar with empty containers.
func TestExtractPortsFromSidecarNoContainers(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	assert.Nil(t, ports)
}

// TestExtractPortsFromSidecarSinglePort tests extractPortsFromSidecar with a single port.
func TestExtractPortsFromSidecarSinglePort(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     8080,
						ContainerPortName: "http",
						Protocol:          types.ProtoTCP,
						AgentPort:         8081,
					},
				},
			},
		},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	require.Len(t, ports, 1)
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "http",
		ContainerPort:     8080,
		Protocol:          "TCP",
		ServicePortName:   "",
		ServicePort:       0,
	}, ports[0])
}

// TestExtractPortsFromSidecarMultiplePorts tests extractPortsFromSidecar with multiple ports.
func TestExtractPortsFromSidecarMultiplePorts(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     8080,
						ContainerPortName: "http",
						Protocol:          types.ProtoTCP,
						AgentPort:         8081,
					},
					{
						ContainerPort:     9090,
						ContainerPortName: "metrics",
						Protocol:          types.ProtoTCP,
						AgentPort:         9091,
					},
				},
			},
		},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	require.Len(t, ports, 2)
	// Check that both ports are present (order may vary)
	portMap := make(map[int32]*rpc.WorkloadPortInfo)
	for _, p := range ports {
		portMap[p.ContainerPort] = p
	}
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "http",
		ContainerPort:     8080,
		Protocol:          "TCP",
		ServicePortName:   "",
		ServicePort:       0,
	}, portMap[8080])
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "metrics",
		ContainerPort:     9090,
		Protocol:          "TCP",
		ServicePortName:   "",
		ServicePort:       0,
	}, portMap[9090])
}

// TestExtractPortsFromSidecarMultipleContainers tests extractPortsFromSidecar with multiple containers.
func TestExtractPortsFromSidecarMultipleContainers(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     8080,
						ContainerPortName: "http",
						Protocol:          types.ProtoTCP,
						AgentPort:         8081,
					},
				},
			},
			{
				Name: "worker",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     5000,
						ContainerPortName: "grpc",
						Protocol:          types.ProtoTCP,
						AgentPort:         5001,
					},
				},
			},
		},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	require.Len(t, ports, 2)
	portMap := make(map[int32]*rpc.WorkloadPortInfo)
	for _, p := range ports {
		portMap[p.ContainerPort] = p
	}
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "http",
		ContainerPort:     8080,
		Protocol:          "TCP",
		ServicePortName:   "",
		ServicePort:       0,
	}, portMap[8080])
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "grpc",
		ContainerPort:     5000,
		Protocol:          "TCP",
		ServicePortName:   "",
		ServicePort:       0,
	}, portMap[5000])
}

// TestExtractPortsFromSidecarDuplicatePortSameProtocol tests that duplicate ports with same protocol are deduplicated.
func TestExtractPortsFromSidecarDuplicatePortSameProtocol(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     8080,
						ContainerPortName: "http",
						Protocol:          types.ProtoTCP,
						AgentPort:         8081,
					},
					{
						// Same port and protocol, should be deduplicated
						ContainerPort:     8080,
						ContainerPortName: "http",
						Protocol:          types.ProtoTCP,
						AgentPort:         8082,
					},
				},
			},
		},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	// Should only have one port even though there are two intercepts
	require.Len(t, ports, 1)
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "http",
		ContainerPort:     8080,
		Protocol:          "TCP",
		ServicePortName:   "",
		ServicePort:       0,
	}, ports[0])
}

// TestExtractPortsFromSidecarTCPandUDP tests that same port with TCP and UDP are both included.
func TestExtractPortsFromSidecarTCPandUDP(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     5353,
						ContainerPortName: "dns",
						Protocol:          types.ProtoTCP,
						AgentPort:         5354,
					},
					{
						// Same port, different protocol
						ContainerPort:     5353,
						ContainerPortName: "dns",
						Protocol:          types.ProtoUDP,
						AgentPort:         5355,
					},
				},
			},
		},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	// Should have both TCP and UDP entries
	require.Len(t, ports, 2)
	protocolMap := make(map[string]*rpc.WorkloadPortInfo)
	for _, p := range ports {
		protocolMap[p.Protocol] = p
	}
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "dns",
		ContainerPort:     5353,
		Protocol:          "TCP",
		ServicePortName:   "",
		ServicePort:       0,
	}, protocolMap["TCP"])
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "dns",
		ContainerPort:     5353,
		Protocol:          "UDP",
		ServicePortName:   "",
		ServicePort:       0,
	}, protocolMap["UDP"])
}

// TestExtractPortsFromSidecarNoPortName tests port extraction when ContainerPortName is empty.
func TestExtractPortsFromSidecarNoPortName(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     8080,
						ContainerPortName: "", // No port name
						Protocol:          types.ProtoTCP,
						AgentPort:         8081,
					},
				},
			},
		},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	require.Len(t, ports, 1)
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "",
		ContainerPort:     8080,
		Protocol:          "TCP",
		ServicePortName:   "",
		ServicePort:       0,
	}, ports[0])
}

// TestExtractPortsFromSidecarComplexScenario tests a complex scenario with multiple containers,
// multiple ports, duplicate deduplication, and TCP/UDP.
func TestExtractPortsFromSidecarComplexScenario(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     8080,
						ContainerPortName: "http",
						Protocol:          types.ProtoTCP,
						AgentPort:         8081,
					},
					{
						// Duplicate port/protocol
						ContainerPort:     8080,
						ContainerPortName: "http",
						Protocol:          types.ProtoTCP,
						AgentPort:         8082,
					},
					{
						// Same port, different protocol
						ContainerPort:     8080,
						ContainerPortName: "http",
						Protocol:          types.ProtoUDP,
						AgentPort:         8083,
					},
				},
			},
			{
				Name: "metrics",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     9090,
						ContainerPortName: "metrics",
						Protocol:          types.ProtoTCP,
						AgentPort:         9091,
					},
					{
						ContainerPort:     5353,
						ContainerPortName: "dns",
						Protocol:          types.ProtoTCP,
						AgentPort:         5354,
					},
				},
			},
		},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	// Expected: 8080 TCP, 8080 UDP, 9090 TCP, 5353 TCP (4 ports total)
	require.Len(t, ports, 4)

	portProtocolMap := make(map[string]int32)
	for _, p := range ports {
		key := p.ContainerPortName + "_" + p.Protocol
		portProtocolMap[key] = p.ContainerPort
	}

	assert.Equal(t, int32(8080), portProtocolMap["http_TCP"])
	assert.Equal(t, int32(8080), portProtocolMap["http_UDP"])
	assert.Equal(t, int32(9090), portProtocolMap["metrics_TCP"])
	assert.Equal(t, int32(5353), portProtocolMap["dns_TCP"])
}

// TestExtractPortsFromSidecarWithServicePorts tests port extraction with both container and service ports.
func TestExtractPortsFromSidecarWithServicePorts(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     8080,
						ContainerPortName: "http",
						ServicePort:       80,
						ServicePortName:   "web",
						Protocol:          types.ProtoTCP,
						AgentPort:         8081,
					},
					{
						ContainerPort:     9090,
						ContainerPortName: "metrics",
						ServicePort:       9000,
						ServicePortName:   "metrics-svc",
						Protocol:          types.ProtoTCP,
						AgentPort:         9091,
					},
				},
			},
		},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	require.Len(t, ports, 2)
	portMap := make(map[int32]*rpc.WorkloadPortInfo)
	for _, p := range ports {
		portMap[p.ContainerPort] = p
	}
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "http",
		ContainerPort:     8080,
		Protocol:          "TCP",
		ServicePortName:   "web",
		ServicePort:       80,
	}, portMap[8080])
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "metrics",
		ContainerPort:     9090,
		Protocol:          "TCP",
		ServicePortName:   "metrics-svc",
		ServicePort:       9000,
	}, portMap[9090])
}

// TestExtractPortsFromSidecarHeadlessService tests port extraction for headless services (no service port).
func TestExtractPortsFromSidecarHeadlessService(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)
	sc := &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     8080,
						ContainerPortName: "http",
						ServicePort:       0, // Headless service has no service port
						ServicePortName:   "",
						Protocol:          types.ProtoTCP,
						AgentPort:         8081,
					},
				},
			},
		},
	}
	ports := extractPortsFromSidecar(ctx, sc)
	require.Len(t, ports, 1)
	assert.Equal(t, &rpc.WorkloadPortInfo{
		ContainerPortName: "http",
		ContainerPort:     8080,
		Protocol:          "TCP",
		ServicePortName:   "",
		ServicePort:       0,
	}, ports[0])
}

// TestPortAndProtoAsMapKey tests that types.PortAndProto works correctly as a map key.
func TestPortAndProtoAsMapKey(t *testing.T) {
	key1 := types.PortAndProto{Port: 8080, Proto: types.ProtoTCP}
	key2 := types.PortAndProto{Port: 8080, Proto: types.ProtoTCP}
	key3 := types.PortAndProto{Port: 8080, Proto: types.ProtoUDP}

	m := make(map[types.PortAndProto]bool)
	m[key1] = true

	// Same port and protocol should be found
	assert.True(t, m[key2])

	// Different protocol should not be found
	assert.False(t, m[key3])

	// Add the third key
	m[key3] = true
	assert.True(t, m[key3])
}

// TestExtractPortsFromSidecarProtocolStrings tests that protocol is correctly converted to string.
func TestExtractPortsFromSidecarProtocolStrings(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)

	testCases := []struct {
		name     string
		protocol types.Proto
		expected string
	}{
		{
			name:     "TCP protocol",
			protocol: types.ProtoTCP,
			expected: "TCP",
		},
		{
			name:     "UDP protocol",
			protocol: types.ProtoUDP,
			expected: "UDP",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sc := &agentconfig.Sidecar{
				Containers: []*agentconfig.Container{
					{
						Name: "app",
						Intercepts: []*agentconfig.Intercept{
							{
								ContainerPort:     8080,
								ContainerPortName: "test",
								Protocol:          tc.protocol,
								AgentPort:         8081,
							},
						},
					},
				},
			}
			ports := extractPortsFromSidecar(ctx, sc)
			require.Len(t, ports, 1)
			assert.Equal(t, tc.expected, ports[0].Protocol)
		})
	}
}

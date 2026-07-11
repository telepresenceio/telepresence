package agent

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
	k8sTypes "k8s.io/apimachinery/pkg/types"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/forwarder"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// fakeConfig is a minimal Config that lets newPortHandler run without a real
// pod, procfs, or CRI environment. podIP is configurable per test so both
// the IPv4 and IPv6 loopback selections can be exercised.
type fakeConfig struct {
	sidecar *agentconfig.Sidecar
	podIP   netip.Addr
}

func (f *fakeConfig) AgentConfig() *agentconfig.Sidecar { return f.sidecar }
func (f *fakeConfig) Annotations() map[string]string    { return nil }

func (f *fakeConfig) AppEnviron(context.Context, *agentconfig.Container) (map[string]string, error) {
	return nil, nil
}

func (f *fakeConfig) HasRemoteMounts() bool                      { return false }
func (f *fakeConfig) PodName() string                            { return "test-pod" }
func (f *fakeConfig) PodIP() netip.Addr                          { return f.podIP }
func (f *fakeConfig) PodUID() k8sTypes.UID                       { return "" }
func (f *fakeConfig) NodeAgent() bool                            { return false }
func (f *fakeConfig) AppPodIP() netip.Addr                       { return f.podIP }
func (f *fakeConfig) ListenerFactory() forwarder.ListenerFactory { return nil }
func (f *fakeConfig) DialerFactory() forwarder.Dialer            { return nil }

// newTestContainerState builds a containerState for a single-container,
// single-intercept Sidecar config, with the intercept's TargetPortNumeric
// set as requested, so newPortHandler's defaultTarget selection can be
// exercised directly without a live pod or netfilter ruleset.
func newTestContainerState(t *testing.T, podIP netip.Addr, numericTarget bool) *containerState {
	t.Helper()
	ic := &agentconfig.Intercept{
		ContainerPortName: "http",
		ServicePortName:   "http",
		ServicePort:       80,
		Protocol:          types.ProtoTCP,
		AgentPort:         9900,
		ContainerPort:     8080,
		TargetPortNumeric: numericTarget,
	}
	cn := &agentconfig.Container{
		Name:       "app",
		Intercepts: []*agentconfig.Intercept{ic},
	}
	sidecar := &agentconfig.Sidecar{
		AgentName:  "test",
		Containers: []*agentconfig.Container{cn},
	}
	s, err := NewState(context.Background(), &fakeConfig{sidecar: sidecar, podIP: podIP})
	require.NoError(t, err)
	return &containerState{State: s, container: cn}
}

// Test_newPortHandler_passThroughTarget guards the defaultTarget address
// newPortHandler picks for the agent's own pass-through dial (no intercept
// active): the pod IP's proxy port for a numeric target port; for a named
// one, the pod IP's container port when the pod carries no nftables ruleset
// (a named-target-only sidecar — the application may be bound to the pod IP
// exclusively) and loopback when it does (dialing the pod IP would hit the
// unconditional pod-IP redirect gate and loop back into the agent; see the
// comment in newPortHandler).
func Test_newPortHandler_passThroughTarget(t *testing.T) {
	ipv4 := netip.MustParseAddr("192.168.50.34")
	ipv6 := netip.MustParseAddr("fd00::34")
	pp := types.PortAndProto{Proto: types.ProtoTCP, Port: 8080}

	t.Run("named target port dials the IPv4 pod IP without nft redirects", func(t *testing.T) {
		cs := newTestContainerState(t, ipv4, false)
		ph := cs.newPortHandler(context.Background(), pp, cs.container.Intercepts)
		require.Equal(t, netip.AddrPortFrom(ipv4, 8080), ph.Target())
	})

	t.Run("named target port dials the IPv6 pod IP without nft redirects", func(t *testing.T) {
		cs := newTestContainerState(t, ipv6, false)
		ph := cs.newPortHandler(context.Background(), pp, cs.container.Intercepts)
		require.Equal(t, netip.AddrPortFrom(ipv6, 8080), ph.Target())
	})

	t.Run("named target port dials loopback under nft redirects", func(t *testing.T) {
		cs := newTestContainerState(t, ipv4, false)
		// A numeric-target intercept on another port makes the pod carry the
		// nftables ruleset, so the named port's pass-through must use the
		// loopback exemption.
		cs.container.Intercepts = append(cs.container.Intercepts, &agentconfig.Intercept{
			ContainerPortName: "",
			ServicePortName:   "metrics",
			ServicePort:       81,
			Protocol:          types.ProtoTCP,
			AgentPort:         9901,
			ContainerPort:     8081,
			TargetPortNumeric: true,
		})
		ph := cs.newPortHandler(context.Background(), pp, cs.container.Intercepts[:1])
		require.Equal(t, netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 8080), ph.Target())
	})

	t.Run("numeric target port dials the pod IP's proxy port", func(t *testing.T) {
		cs := newTestContainerState(t, ipv4, true)
		ph := cs.newPortHandler(context.Background(), pp, cs.container.Intercepts)
		wantPort := cs.AgentConfig().ProxyPort(cs.container.Intercepts[0].AgentPort)
		require.Equal(t, netip.AddrPortFrom(ipv4, wantPort), ph.Target())
	})
}

package tls

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestNewManagerConfiguresHTTPAppProtocolWithoutProbing(t *testing.T) {
	for _, appProtocol := range []string{
		"http",
		"kubernetes.io/http",
		"http1",
		"http1.0",
		"http1.1",
		"http/1.0",
		"http/1.1",
	} {
		t.Run(appProtocol, func(t *testing.T) {
			sidecar := sidecarWithAppProtocol(appProtocol)
			mgr, err := NewManager(t.Context(), sidecar, netip.MustParseAddr("10.0.0.1"), nil, nil)
			require.NoError(t, err)

			tlsManager := mgr.(*manager)
			proxyPort := sidecar.ProxyPort(9900)
			pc, ok := tlsManager.portConfigs[proxyPort]
			require.True(t, ok)
			require.Equal(t, ValueNotSupported, pc.TLS)
			require.Equal(t, ValueNotSupported, pc.HTTP2)
		})
	}
}

func TestNewManagerConfiguresExplicitAppProtocols(t *testing.T) {
	tests := []struct {
		name           string
		appProtocol    string
		wantPortConfig bool
		wantTLS        ValueState
		wantHTTP2      ValueState
	}{
		{
			name:           "unknown",
			wantPortConfig: true,
			wantTLS:        ValueUnknown,
			wantHTTP2:      ValueUnknown,
		},
		{
			name:           "https",
			appProtocol:    "https",
			wantPortConfig: true,
			wantTLS:        ValueSupported,
			wantHTTP2:      ValueUnknown,
		},
		{
			name:           "h2c",
			appProtocol:    "h2c",
			wantPortConfig: true,
			wantTLS:        ValueNotSupported,
			wantHTTP2:      ValueSupported,
		},
		{
			name:           "tcp",
			appProtocol:    "tcp",
			wantPortConfig: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sidecar := sidecarWithAppProtocol(tt.appProtocol)
			mgr, err := NewManager(t.Context(), sidecar, netip.MustParseAddr("10.0.0.1"), nil, nil)
			require.NoError(t, err)

			tlsManager := mgr.(*manager)
			proxyPort := sidecar.ProxyPort(9900)
			pc, ok := tlsManager.portConfigs[proxyPort]
			require.Equal(t, tt.wantPortConfig, ok)
			if !tt.wantPortConfig {
				return
			}
			require.Equal(t, tt.wantTLS, pc.TLS)
			require.Equal(t, tt.wantHTTP2, pc.HTTP2)
		})
	}
}

// recordingDialer implements forwarder.Dialer, recording every address it is asked to
// dial and dialing a fixed real listener instead of that address, so a probe can run to
// completion against a stub "application" without a real pod or netfilter ruleset.
type recordingDialer struct {
	mu    sync.Mutex
	calls []string
	real  string
}

func (d *recordingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.calls = append(d.calls, address)
	d.mu.Unlock()
	var nd net.Dialer
	return nd.DialContext(ctx, network, d.real)
}

func (d *recordingDialer) dialedAddrs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// TestNewManagerProbesThroughInjectedDialer guards the fix for the node-agent bug where
// the TLS/H2C prober dialed the agent's own pod IP: it must dial through the injected
// Dialer, at the address agentconfig.PassThroughTarget selects for the intercept's
// container port -- the same address a pass-through forwarder would dial -- not at the
// address of whatever pod the agent's own process happens to run in.
func TestNewManagerProbesThroughInjectedDialer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	dialer := &recordingDialer{real: ln.Addr().String()}
	sidecar := sidecarWithAppProtocol("") // unknown app protocol: TLS/HTTP2 must be probed.
	appPodIP := netip.MustParseAddr("10.9.9.9")

	mgr, err := NewManager(t.Context(), sidecar, appPodIP, dialer, nil)
	require.NoError(t, err)

	proxyPort := sidecar.ProxyPort(9900)
	mgr.UseTLS(t.Context(), proxyPort)

	// A non-nil dialer marks the node-agent seam, where the nft redirects are
	// always programmed.
	wantTarget := sidecar.PassThroughTarget(appPodIP, 8000, types.ProtoTCP, true)
	calls := dialer.dialedAddrs()
	require.NotEmpty(t, calls)
	for _, addr := range calls {
		require.Equal(t, wantTarget.String(), addr)
	}
}

func sidecarWithAppProtocol(appProtocol string) *agentconfig.Sidecar {
	return &agentconfig.Sidecar{
		Containers: []*agentconfig.Container{
			{
				Name: "app",
				Intercepts: []*agentconfig.Intercept{
					{
						ContainerPort:     8000,
						ServicePort:       8000,
						AgentPort:         9900,
						Protocol:          types.ProtoTCP,
						AppProtocol:       appProtocol,
						TargetPortNumeric: true,
					},
				},
			},
		},
	}
}

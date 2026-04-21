package tls

import (
	"net/netip"
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
			mgr, err := NewManager(t.Context(), sidecar, netip.MustParseAddr("10.0.0.1"), nil)
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
			mgr, err := NewManager(t.Context(), sidecar, netip.MustParseAddr("10.0.0.1"), nil)
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

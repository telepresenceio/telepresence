package cmd

import (
	"bytes"
	"encoding/json/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	daemonRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestStatusInfoDurationJSON(t *testing.T) {
	s := &StatusInfo{RootDaemon: RootDaemonStatus{
		Running: true,
		DNS:     &client.DNSSnake{LookupTimeout: 4 * time.Second},
	}}
	data, err := s.MarshalJSON()
	require.NoError(t, err)
	assert.Contains(t, string(data), `"lookup_timeout":"4s"`)
}

func TestFormatTunnelTransport(t *testing.T) {
	tests := []struct {
		name string
		tt   *daemonRpc.TunnelTransport
		want string
	}{
		{"nil, old daemon", nil, ""},
		{"zero value", &daemonRpc.TunnelTransport{}, ""},
		{"grpc, no endpoint", &daemonRpc.TunnelTransport{Transport: "grpc"}, "grpc"},
		{"quic with endpoint", &daemonRpc.TunnelTransport{Transport: "quic", Endpoint: "1.2.3.4:7778"}, "quic (1.2.3.4:7778)"},
		{"fallback, no endpoint", &daemonRpc.TunnelTransport{Transport: "grpc (fallback)"}, "grpc (fallback)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, formatTunnelTransport(tc.tt))
		})
	}
}

// TestRootDaemonStatus_TunnelTransport verifies that the tunnel transport line
// appears in both the text and JSON status output when known, and is omitted
// entirely (degrading gracefully against an old root daemon) when empty.
func TestRootDaemonStatus_TunnelTransport(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		ds := &RootDaemonStatus{Running: true, Name: "Root Daemon", TunnelTransport: "quic (1.2.3.4:7778)"}

		buf := &bytes.Buffer{}
		_, err := ds.WriteTo(buf)
		require.NoError(t, err)
		assert.Contains(t, buf.String(), "Tunnel transport")
		assert.Contains(t, buf.String(), "quic (1.2.3.4:7778)")

		js, err := json.Marshal(ds)
		require.NoError(t, err)
		assert.Contains(t, string(js), `"tunnel_transport":"quic (1.2.3.4:7778)"`)
	})

	t.Run("absent", func(t *testing.T) {
		ds := &RootDaemonStatus{Running: true, Name: "Root Daemon"}

		buf := &bytes.Buffer{}
		_, err := ds.WriteTo(buf)
		require.NoError(t, err)
		assert.NotContains(t, buf.String(), "Tunnel transport")

		js, err := json.Marshal(ds)
		require.NoError(t, err)
		assert.NotContains(t, string(js), "tunnel_transport")
	})
}

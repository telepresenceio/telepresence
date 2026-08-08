package trafficmgr

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

// fakeQuicTunnelEndpointGetter scripts a GetQuicTunnelEndpoint response (or error) and
// records whether it was called, so a test can assert requireQuicTunnelAvailable skips
// the RPC entirely for a non-external connection.
type fakeQuicTunnelEndpointGetter struct {
	ep      *manager.QuicTunnelEndpoint
	err     error
	called  bool
	lastReq *manager.SessionInfo
}

func (f *fakeQuicTunnelEndpointGetter) GetQuicTunnelEndpoint(
	_ context.Context, in *manager.SessionInfo, _ ...grpc.CallOption,
) (*manager.QuicTunnelEndpoint, error) {
	f.called = true
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.ep, nil
}

func externalConfigCtx(t *testing.T) context.Context {
	t.Helper()
	cfg := client.GetDefaultConfig()
	cfg.Cluster().ManagerAddress = "tls://tm.example.com:8443"
	return client.WithConfig(context.Background(), cfg)
}

func localConfigCtx(t *testing.T) context.Context {
	t.Helper()
	return client.WithConfig(context.Background(), client.GetDefaultConfig())
}

// TestRequireQuicTunnelAvailable_NonExternal asserts that a connection without
// cluster.managerAddress set never calls the manager: the port-forwarded gRPC channel
// already reaches the agent, so there is nothing to check.
func TestRequireQuicTunnelAvailable_NonExternal(t *testing.T) {
	mc := &fakeQuicTunnelEndpointGetter{ep: &manager.QuicTunnelEndpoint{Enabled: false}}
	err := requireQuicTunnelAvailable(localConfigCtx(t), mc, &manager.SessionInfo{SessionId: "s1"}, "intercept")
	require.NoError(t, err)
	require.False(t, mc.called)
}

// TestRequireQuicTunnelAvailable_ExternalDisabled asserts that an external connection
// whose manager reports no published QUIC endpoint is refused with an actionable,
// fragment-stable error.
func TestRequireQuicTunnelAvailable_ExternalDisabled(t *testing.T) {
	mc := &fakeQuicTunnelEndpointGetter{ep: &manager.QuicTunnelEndpoint{Enabled: false}}
	si := &manager.SessionInfo{SessionId: "s1"}
	err := requireQuicTunnelAvailable(externalConfigCtx(t), mc, si, "intercept")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires a channel to the traffic-agent")
	require.Contains(t, err.Error(), "quicTunnel")
	require.True(t, mc.called)
	require.Same(t, si, mc.lastReq)
}

// TestRequireQuicTunnelAvailable_ExternalEnabled asserts that an external connection
// whose manager reports a published QUIC endpoint is allowed through.
func TestRequireQuicTunnelAvailable_ExternalEnabled(t *testing.T) {
	mc := &fakeQuicTunnelEndpointGetter{ep: &manager.QuicTunnelEndpoint{Enabled: true}}
	err := requireQuicTunnelAvailable(externalConfigCtx(t), mc, &manager.SessionInfo{SessionId: "s1"}, "ingest")
	require.NoError(t, err)
	require.True(t, mc.called)
}

// TestRequireQuicTunnelAvailable_ExternalRPCError asserts that an external connection
// that can't even ask the manager (old manager, transient failure) fails closed rather
// than silently allowing an attachment that may have no data-plane channel.
func TestRequireQuicTunnelAvailable_ExternalRPCError(t *testing.T) {
	mc := &fakeQuicTunnelEndpointGetter{err: errors.New("unimplemented")}
	err := requireQuicTunnelAvailable(externalConfigCtx(t), mc, &manager.SessionInfo{SessionId: "s1"}, "replace")
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires a channel to the traffic-agent")
	require.Contains(t, err.Error(), "quicTunnel")
}

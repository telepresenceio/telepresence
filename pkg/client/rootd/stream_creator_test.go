package rootd

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestCreateSessionCleansDNSRoutingBeforeKubeconfig(t *testing.T) {
	ctx := client.WithConfig(context.Background(), client.GetDefaultConfig())

	called := false
	cleanupDNSRouting := func(context.Context) {
		called = true
	}

	_, err := createSession(ctx, ctx, &rpc.NetworkConfig{
		KubeconfigData: []byte("not: [valid"),
	}, make(chan time.Time), cleanupDNSRouting)

	require.Error(t, err)
	require.True(t, called)
}

func TestIsAlsoProxyDestination(t *testing.T) {
	s := &session{
		alsoProxySubnets: []netip.Prefix{
			netip.MustParsePrefix("240.240.0.0/16"),
			netip.MustParsePrefix("fd00::/8"),
		},
	}

	require.True(t, s.isAlsoProxyDestination(netip.MustParseAddr("240.240.0.33")))
	require.True(t, s.isAlsoProxyDestination(netip.MustParseAddr("fd00::1")))
	require.False(t, s.isAlsoProxyDestination(netip.MustParseAddr("10.128.0.10")))
	require.False(t, s.isAlsoProxyDestination(netip.MustParseAddr("2001:db8::1")))
}

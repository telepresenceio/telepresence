package setup

import (
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	rpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestRoutesFromStatus(t *testing.T) {
	cfg := client.GetDefaultConfig()
	cfg.Routing().Subnets = []netip.Prefix{netip.MustParsePrefix("10.96.0.0/16")}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)

	_, _, ok := routesFromStatus(nil)
	assert.False(t, ok, "nil status")

	_, _, ok = routesFromStatus(&connector.ConnectInfo{})
	assert.False(t, ok, "no root daemon status")

	_, _, ok = routesFromStatus(&connector.ConnectInfo{DaemonStatus: &rpc.DaemonStatus{InUse: true}})
	assert.False(t, ok, "root daemon in use by another caller")

	subnets, ifName, ok := routesFromStatus(&connector.ConnectInfo{DaemonStatus: &rpc.DaemonStatus{
		OutboundConfig:   &rpc.NetworkConfig{ClientConfig: data},
		VifInterfaceName: "tel0",
	}})
	require.True(t, ok)
	assert.Equal(t, "tel0", ifName)
	assert.Equal(t, []netip.Prefix{netip.MustParsePrefix("10.96.0.0/16")}, subnets)
}

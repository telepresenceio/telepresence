package routing

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/datawire/dlib/dlog"
)

func TestGetRoutingTable_defaultRoute(t *testing.T) {
	ctx := dlog.NewTestContext(t, true)
	rt, err := GetRoutingTable(ctx)
	assert.NoError(t, err)
	var dflt *Route
	for _, r := range rt {
		if r.Default {
			dflt = r
			break
		}
	}
	assert.NotNil(t, dflt)
	assert.NotEqual(t, netip.IPv4Unspecified(), dflt.Gateway)
	assert.NotEqual(t, netip.IPv6Unspecified(), dflt.Gateway)
}

func TestGetRoutingTable(t *testing.T) {
	ctx := dlog.NewTestContext(t, true)
	rt, err := GetRoutingTable(ctx)
	assert.NoError(t, err)
	assert.NotEmpty(t, rt)
	for _, r := range rt {
		assert.True(t, r.LocalIP.IsValid())
		assert.NotNil(t, r.Interface)
		assert.True(t, r.RoutedNet.IsValid())
	}
}

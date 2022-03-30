package routing

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/datawire/dlib/dlog"
)

func TestGetRoutingTable_defaultRoute(t *testing.T) {
	ctx := dlog.NewTestContext(t, true)
	rt, err := GetRoutingTable(ctx)
	assert.NoError(t, err)
	var dflt *Route
	for i := range rt {
		r := &rt[i]
		if r.Default {
			dflt = r
			break
		}
	}
	assert.NotNil(t, dflt)
	assert.False(t, dflt.Gateway.Equal(net.IP{0, 0, 0, 0}))
}

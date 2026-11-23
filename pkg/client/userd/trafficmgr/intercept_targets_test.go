package trafficmgr

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestInterceptTargets(t *testing.T) {
	ii := func(d manager.InterceptDispositionType, host string, port int32, proto string) *manager.InterceptInfo {
		return &manager.InterceptInfo{
			Disposition: d,
			Spec:        &manager.InterceptSpec{Namespace: "ns", Agent: "echo", TargetHost: host, TargetPort: port, Protocol: proto},
		}
	}
	got := interceptTargets([]*manager.InterceptInfo{
		ii(manager.InterceptDispositionType_ACTIVE, "127.0.0.1", 8080, ""),
		ii(manager.InterceptDispositionType_WAITING, "fd00::1", 9090, "UDP"),
		ii(manager.InterceptDispositionType_NO_AGENT, "127.0.0.1", 1, ""),
		ii(manager.InterceptDispositionType_ACTIVE, "not-an-ip", 1, ""),
		ii(manager.InterceptDispositionType_ACTIVE, "127.0.0.1", 0, ""),
	})
	require.Len(t, got, 2)
	var ap netip.AddrPort
	require.NoError(t, ap.UnmarshalBinary(got[0].Target))
	assert.Equal(t, "127.0.0.1:8080", ap.String())
	assert.Equal(t, "TCP", got[0].Protocol)
	assert.Equal(t, "ns", got[0].Namespace)
	assert.Equal(t, "echo", got[0].Workload)
	require.NoError(t, ap.UnmarshalBinary(got[1].Target))
	assert.Equal(t, "[fd00::1]:9090", ap.String())
	assert.Equal(t, "UDP", got[1].Protocol)
}

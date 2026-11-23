package trafficmgr

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestInterceptTargets(t *testing.T) {
	// The cluster path cannot play a hostile traffic-manager, so the target set is checked here.
	ii := func(d manager.InterceptDispositionType, host string, port int32, proto string) *manager.InterceptInfo {
		return &manager.InterceptInfo{
			Disposition: d,
			Spec:        &manager.InterceptSpec{Namespace: "ns", Agent: "echo", TargetHost: host, TargetPort: port, Protocol: proto},
		}
	}
	named := func(name string, i *manager.InterceptInfo) *manager.InterceptInfo {
		i.Spec.Name = name
		return i
	}
	got := interceptTargets([]*manager.InterceptInfo{
		named("a", ii(manager.InterceptDispositionType_ACTIVE, "127.0.0.1", 8080, "")),
		named("b", ii(manager.InterceptDispositionType_WAITING, "fd00::1", 9090, "UDP")),
		named("c", ii(manager.InterceptDispositionType_NO_AGENT, "127.0.0.1", 1, "")),
		named("d", ii(manager.InterceptDispositionType_ACTIVE, "not-an-ip", 1, "")),
		named("e", ii(manager.InterceptDispositionType_ACTIVE, "127.0.0.1", 0, "")),
	}, map[string]destinationSet{
		"a": newDestinationSet("127.0.0.1", 8080, "", nil),
		"b": newDestinationSet("fd00::1", 9090, "UDP", nil),
		"c": newDestinationSet("127.0.0.1", 1, "", nil),
		"d": newDestinationSet("not-an-ip", 1, "", nil),
		"e": newDestinationSet("127.0.0.1", 0, "", nil),
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

	t.Run("pod ports and service workloads", func(t *testing.T) {
		got := interceptTargets([]*manager.InterceptInfo{{
			Disposition: manager.InterceptDispositionType_ACTIVE,
			Spec: &manager.InterceptSpec{
				Name: "x", Namespace: "ns", Agent: "echo", TargetHost: "127.0.0.1", TargetPort: 8080,
				PodPorts: []string{"9000:9001", "9100:9101/UDP"},
			},
			ServiceWorkloads: []*manager.InterceptWorkload{
				{Namespace: "ns", WorkloadName: "echo"},
				{Namespace: "ns", WorkloadName: "other"},
			},
		}}, map[string]destinationSet{
			"x": newDestinationSet("127.0.0.1", 8080, "", []string{"9000:9001", "9100:9101/UDP"}),
		})
		seen := map[string]bool{}
		for _, tg := range got {
			var ap netip.AddrPort
			require.NoError(t, ap.UnmarshalBinary(tg.Target))
			seen[tg.Workload+" "+tg.Protocol+" "+ap.String()] = true
		}
		assert.Len(t, got, 6)
		for _, wl := range []string{"echo", "other"} {
			assert.True(t, seen[wl+" TCP 127.0.0.1:8080"])
			assert.True(t, seen[wl+" TCP 127.0.0.1:9001"])
			assert.True(t, seen[wl+" UDP 127.0.0.1:9101"])
		}
	})

	t.Run("destinations the local request did not ask for", func(t *testing.T) {
		snap := func(name, host string, port int32, podPorts ...string) *manager.InterceptInfo {
			return &manager.InterceptInfo{
				Disposition: manager.InterceptDispositionType_ACTIVE,
				Spec: &manager.InterceptSpec{
					Name: name, Namespace: "ns", Agent: "echo", TargetHost: host, TargetPort: port, PodPorts: podPorts,
				},
			}
		}
		dests := map[string]destinationSet{
			"mine": newDestinationSet("127.0.0.1", 8080, "", []string{"9000:9001"}),
		}
		// No local record.
		assert.Empty(t, interceptTargets([]*manager.InterceptInfo{snap("other", "127.0.0.1", 5432)}, dests))
		// Other host, other port, other pod port than the local record.
		assert.Empty(t, interceptTargets([]*manager.InterceptInfo{snap("mine", "192.168.1.10", 8080)}, dests))
		assert.Empty(t, interceptTargets([]*manager.InterceptInfo{snap("mine", "127.0.0.1", 5432)}, dests))
		got := interceptTargets([]*manager.InterceptInfo{snap("mine", "127.0.0.1", 8080, "9000:9001", "9100:5432")}, dests)
		require.Len(t, got, 2)
		var ap netip.AddrPort
		require.NoError(t, ap.UnmarshalBinary(got[1].Target))
		assert.Equal(t, "127.0.0.1:9001", ap.String())
	})
}

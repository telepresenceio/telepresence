package trafficmgr

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
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
		}})
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
}

func TestRestoredInterceptFromSnapshot(t *testing.T) {
	// The cluster path cannot feed the handler malformed entries, so the snapshot handling is checked here.
	s := newWorkloadSnapshotTestSession(t, map[string]*intercept{})
	s.Context = client.WithConfig(s.Context, client.GetDefaultConfig())
	s.interceptWaiters = map[string]*awaitIntercept{}

	restored := &manager.InterceptInfo{
		Id:               "x",
		Disposition:      manager.InterceptDispositionType_ACTIVE,
		PodIp:            "10.0.0.9",
		ClientMountPoint: "/home/user",
		Spec: &manager.InterceptSpec{
			Name: "restored", Namespace: "ns", Agent: "echo", TargetHost: "127.0.0.1", TargetPort: 8080,
			LocalPorts: []string{"8081"},
		},
	}
	noSpec := &manager.InterceptInfo{Id: "y", Disposition: manager.InterceptDispositionType_ACTIVE}

	// Must not panic on the entry without a spec.
	s.handleInterceptSnapshot(newPodAccessTracker(), []*manager.InterceptInfo{restored, noSpec, nil})

	require.Len(t, s.currentIntercepts, 1)
	ic := s.currentIntercepts["x"]
	require.NotNil(t, ic)
	assert.Empty(t, ic.ClientMountPoint)
	assert.Equal(t, []string{"8081"}, ic.podAccess().localPorts)
	targets := interceptTargets([]*manager.InterceptInfo{restored})
	require.Len(t, targets, 1)

	// A waiter binds only to the entry with its ID.
	s.interceptWaiters["mine"] = &awaitIntercept{id: "sess:mine", mountPoint: "/mnt", localPorts: []string{"9000"}}
	mine := &manager.InterceptInfo{Id: "sess:mine", Spec: &manager.InterceptSpec{Name: "mine"}}
	other := &manager.InterceptInfo{Id: "other:mine", Spec: &manager.InterceptSpec{Name: "mine"}}
	s.setCurrentIntercepts([]*manager.InterceptInfo{other, mine})
	assert.Empty(t, s.currentIntercepts["other:mine"].ClientMountPoint)
	assert.Equal(t, "/mnt", s.currentIntercepts["sess:mine"].ClientMountPoint)
	assert.Equal(t, []string{"9000"}, s.currentIntercepts["sess:mine"].localPorts)

	// A pending name cannot be reserved twice.
	s.currentInterceptsLock.Lock()
	assert.Error(t, s.reserveInterceptNameLocked("mine"))
	assert.NoError(t, s.reserveInterceptNameLocked("free"))
	s.currentInterceptsLock.Unlock()
}

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

func TestForeignInterceptDoesNothingLocal(t *testing.T) {
	// The cluster path cannot play a false traffic-manager, so the snapshot handling is checked here.
	s := newWorkloadSnapshotTestSession(t, map[string]*intercept{})
	s.Context = client.WithConfig(s.Context, client.GetDefaultConfig())
	s.interceptWaiters = map[string]*awaitIntercept{}
	s.localDestinations = map[string]destinationSet{}

	foreign := &manager.InterceptInfo{
		Id:               "x",
		Disposition:      manager.InterceptDispositionType_ACTIVE,
		PodIp:            "10.0.0.9",
		SftpPort:         2222,
		MountPoint:       "/export",
		ClientMountPoint: "/home/user",
		Environment:      map[string]string{"TELEPRESENCE_API_PORT": "9980"},
		Spec: &manager.InterceptSpec{
			Name: "foreign", Namespace: "ns", Agent: "echo", TargetHost: "127.0.0.1", TargetPort: 8080,
			LocalPorts: []string{"8081"},
		},
	}
	noSpec := &manager.InterceptInfo{Id: "y", Disposition: manager.InterceptDispositionType_ACTIVE}

	// Must not panic on the entry without a spec.
	pat := newPodAccessTracker()
	s.handleInterceptSnapshot(pat, []*manager.InterceptInfo{foreign, noSpec, nil})

	require.Len(t, s.currentIntercepts, 1)
	ic := s.currentIntercepts["x"]
	require.NotNil(t, ic)
	assert.False(t, ic.local)
	assert.Empty(t, ic.ClientMountPoint)
	pa := ic.podAccess()
	assert.False(t, pa.shouldMount())
	assert.False(t, pa.shouldForward())
	assert.Empty(t, pat.alivePods)
	assert.Empty(t, s.currentAPIServers)
	assert.Empty(t, s.ownedIntercepts([]*manager.InterceptInfo{foreign}))
	assert.Empty(t, interceptTargets(s.ownedIntercepts([]*manager.InterceptInfo{foreign}), s.localDestinations))
	assert.Empty(t, s.interceptShortcuts(s.ownedIntercepts([]*manager.InterceptInfo{foreign}), true))

	// An entry that matches a pending local request is owned.
	s.interceptWaiters["mine"] = &awaitIntercept{id: "sess:mine"}
	mine := &manager.InterceptInfo{Id: "sess:mine", Spec: &manager.InterceptSpec{Name: "mine"}}
	assert.Len(t, s.ownedIntercepts([]*manager.InterceptInfo{mine}), 1)

	// An entry with the same name but another ID is not.
	other := &manager.InterceptInfo{Id: "evil:mine", Spec: &manager.InterceptSpec{Name: "mine"}}
	assert.Empty(t, s.ownedIntercepts([]*manager.InterceptInfo{other}))
	s.setCurrentIntercepts([]*manager.InterceptInfo{other, mine})
	assert.False(t, s.currentIntercepts["evil:mine"].local)
	assert.True(t, s.currentIntercepts["sess:mine"].local)

	// A pending name cannot be reserved twice.
	s.currentInterceptsLock.Lock()
	assert.Error(t, s.reserveInterceptNameLocked("mine"))
	assert.NoError(t, s.reserveInterceptNameLocked("free"))
	s.currentInterceptsLock.Unlock()
}

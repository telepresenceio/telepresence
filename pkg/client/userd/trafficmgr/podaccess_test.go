package trafficmgr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
)

// fakeWaitForAgentIPClient is a daemon.DaemonClient that only implements WaitForAgentIP;
// ensureAccess doesn't call anything else on the daemon client.
type fakeWaitForAgentIPClient struct {
	daemon.DaemonClient
	rsp *daemon.WaitForAgentIPResponse
	err error
}

func (f *fakeWaitForAgentIPClient) WaitForAgentIP(
	context.Context, *daemon.WaitForAgentIPRequest, ...grpc.CallOption,
) (*daemon.WaitForAgentIPResponse, error) {
	return f.rsp, f.err
}

// TestEnsureAccess_UnavailableBecomesUserError proves that ensureAccess turns a daemon
// response of codes.Unavailable -- the daemon found no direct path to the agent -- into a
// user-facing error that fails the attachment immediately, carrying the daemon's message
// plus the remedy, instead of silently proceeding as if the manager would forward traffic.
func TestEnsureAccess_UnavailableBecomesUserError(t *testing.T) {
	ctx := client.WithConfig(context.Background(), client.GetDefaultConfig())
	pa := &podAccess{
		ctx:       ctx,
		namespace: "alpha",
		podIP:     "10.244.0.7",
	}
	rd := &fakeWaitForAgentIPClient{
		err: status.Error(codes.Unavailable,
			"direct agent access in namespace alpha refused (pods/portforward) and the QUIC tunnel is not available"),
	}

	err := pa.ensureAccess(ctx, rd)
	require.Error(t, err)
	assert.Equal(t, errcat.User, errcat.GetCategory(err))
	assert.Contains(t, err.Error(), "direct agent access in namespace alpha refused (pods/portforward) and the QUIC tunnel is not available")
	assert.Contains(t, err.Error(), "grant pods/portforward in that namespace or enable the QUIC tunnel (quicTunnel.enabled)")
}

// TestEnsureAccess_OKUpdatesPodIP proves the success path is unaffected: a codes.OK
// response still rewrites pa.podIP to the local IP the daemon forwarded to.
func TestEnsureAccess_OKUpdatesPodIP(t *testing.T) {
	ctx := client.WithConfig(context.Background(), client.GetDefaultConfig())
	pa := &podAccess{
		ctx:       ctx,
		namespace: "alpha",
		podIP:     "10.244.0.7",
	}
	rd := &fakeWaitForAgentIPClient{
		rsp: &daemon.WaitForAgentIPResponse{LocalIp: []byte{127, 0, 0, 1}},
	}

	err := pa.ensureAccess(ctx, rd)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", pa.podIP)
}

// alivePodKey seeds lpf.alivePods directly with a no-op podAccessSync so
// cancelUnwanted's scoping can be exercised without going through start's
// mount/port-forward machinery.
func alivePodKey(lpf *podAccessTracker, fk podAccessKey) {
	lpf.alivePods[fk] = &podAccessSync{cancelPod: func() {}}
}

// TestCancelUnwantedNamespaceScoping exercises cancelUnwanted's covered
// predicate: entries in namespaces covered decides against survive, entries
// in namespaces it doesn't cover are cancelled, and covered == nil cancels
// everything left over from the snapshot.
func TestCancelUnwantedNamespaceScoping(t *testing.T) {
	t.Run("uncovered namespace survives, covered one is cancelled", func(t *testing.T) {
		lpf := newPodAccessTracker()
		lpf.initSnapshot() // empty snapshot: nothing was started this round

		covered := podAccessKey{namespace: "covered-ns", podIP: "10.0.0.1"}
		uncovered := podAccessKey{namespace: "uncovered-ns", podIP: "10.0.0.2"}
		alivePodKey(lpf, covered)
		alivePodKey(lpf, uncovered)

		lpf.cancelUnwanted(context.Background(), func(namespace string) bool {
			return namespace == "covered-ns"
		})

		_, coveredStillAlive := lpf.alivePods[covered]
		_, uncoveredStillAlive := lpf.alivePods[uncovered]
		assert.False(t, coveredStillAlive, "an unwanted entry in a covered namespace must be cancelled")
		assert.True(t, uncoveredStillAlive, "an entry in an uncovered namespace must survive unrelated churn")
	})

	t.Run("an entry still in the snapshot survives even when covered", func(t *testing.T) {
		lpf := newPodAccessTracker()
		lpf.initSnapshot()
		fk := podAccessKey{namespace: "ns", podIP: "10.0.0.1"}
		alivePodKey(lpf, fk)
		lpf.snapshot[fk] = struct{}{} // marked wanted this round

		lpf.cancelUnwanted(context.Background(), func(string) bool { return true })

		_, stillAlive := lpf.alivePods[fk]
		assert.True(t, stillAlive)
	})

	t.Run("nil covered cancels every unwanted entry regardless of namespace", func(t *testing.T) {
		lpf := newPodAccessTracker()
		lpf.initSnapshot()
		a := podAccessKey{namespace: "ns-a", podIP: "10.0.0.1"}
		b := podAccessKey{namespace: "ns-b", podIP: "10.0.0.2"}
		alivePodKey(lpf, a)
		alivePodKey(lpf, b)

		lpf.cancelUnwanted(context.Background(), nil)

		assert.Empty(t, lpf.alivePods)
	})
}

package trafficmgr

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

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

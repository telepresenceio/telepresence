package eventwatch

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	events "k8s.io/api/events/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

type recordingWatch struct {
	result   chan watch.Event
	stopped  chan struct{}
	stopOnce sync.Once
}

func newRecordingWatch() *recordingWatch {
	return &recordingWatch{
		result:  make(chan watch.Event, 1),
		stopped: make(chan struct{}),
	}
}

func (w *recordingWatch) Stop() {
	w.stopOnce.Do(func() { close(w.stopped) })
}

func (w *recordingWatch) ResultChan() <-chan watch.Event {
	return w.result
}

func TestWatchWarningsStopsWhenDeliveryIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := newRecordingWatch()
	ki := fake.NewSimpleClientset()
	ki.PrependWatchReactor("events", func(k8stesting.Action) (bool, watch.Interface, error) {
		return true, w, nil
	})

	_, err := WatchWarnings(ctx, ki, "default", "echo")
	require.NoError(t, err)

	// Do not receive from the returned channel. This reproduces waitForAgents
	// returning while a matching event is in flight.
	w.result <- watch.Event{Type: watch.Added, Object: &events.Event{
		ObjectMeta: meta.ObjectMeta{CreationTimestamp: meta.Now()},
		Regarding:  core.ObjectReference{Name: "echo"},
		Type:       "Warning",
		Reason:     "FailedCreate",
		Note:       "quota exceeded",
	}}
	require.Eventually(t, func() bool { return len(w.result) == 0 }, time.Second, time.Millisecond)

	cancel()
	select {
	case <-w.stopped:
	case <-time.After(time.Second):
		t.Fatal("underlying watch was not stopped after cancellation")
	}
}

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		etype  string
		note   string
		want   bool
	}{
		{"backoff is terminal", "BackOff", "Warning", "Back-off restarting failed container", true},
		{"image pull failure is terminal", "Failed", "Warning", `Failed to pull image "tel2:9.9.9": not found`, true},
		{"failed create is terminal", "FailedCreate", "Warning", "create Pod failed: forbidden", true},
		{"unschedulable on resources is terminal", "FailedScheduling", "Warning", "0/3 nodes are available: Insufficient cpu", false},
		{"ephemeral volume wait is transient", "Failed", "Warning", "waiting for ephemeral volume controller to create the persistentvolumeclaim", false},
		{"unbound pvc is transient", "FailedScheduling", "Warning", "0/1 nodes: unbound immediate PersistentVolumeClaims", false},
		{"skip schedule deleting pod is transient", "FailedScheduling", "Warning", "skip schedule deleting pod: ns/p", false},
		{"nodes are available is transient", "FailedScheduling", "Warning", "0/3 nodes are available", false},
		{"unknown reason is not terminal", "SomethingElse", "Warning", "whatever", false},
		{"non-warning transient note is still terminal", "Failed", "Normal", "nodes are available", true},
		{"bind plugin race during rollout is transient", "FailedScheduling", "Warning", `running Bind plugin "DefaultBinder": pods "hello-abc123" not found`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &events.Event{Reason: tt.reason, Type: tt.etype, Note: tt.note}
			assert.Equal(t, tt.want, IsTerminal(e))
		})
	}
}

func TestListWarnings(t *testing.T) {
	client := fake.NewClientset(
		&events.Event{
			ObjectMeta: meta.ObjectMeta{Name: "e1", Namespace: "ns"},
			Regarding:  core.ObjectReference{Kind: "Pod", Name: "traffic-manager-abc"},
			Type:       "Warning",
			Reason:     "BackOff",
			Note:       "Back-off restarting failed container",
		},
		&events.Event{
			ObjectMeta: meta.ObjectMeta{Name: "e2", Namespace: "ns"},
			Regarding:  core.ObjectReference{Kind: "Pod", Name: "traffic-manager-abc"},
			Type:       "Warning",
			Reason:     "BackOff",
			Note:       "(combined from similar events): Back-off restarting failed container",
		},
		&events.Event{
			ObjectMeta: meta.ObjectMeta{Name: "e3", Namespace: "ns"},
			Regarding:  core.ObjectReference{Kind: "Pod", Name: "other-abc"},
			Type:       "Warning",
			Reason:     "BackOff",
			Note:       "Back-off restarting failed container",
		},
		&events.Event{
			ObjectMeta: meta.ObjectMeta{Name: "e4", Namespace: "ns"},
			Regarding:  core.ObjectReference{Kind: "Service", Name: "traffic-manager-quic"},
			Type:       "Warning",
			Reason:     "SomeReason",
			Note:       "unrelated service sharing the name prefix",
		},
		&events.Event{
			ObjectMeta: meta.ObjectMeta{Name: "e5", Namespace: "ns"},
			Regarding:  core.ObjectReference{Kind: "ReplicaSet", Name: "traffic-manager-abc"},
			Type:       "Warning",
			Reason:     "FailedCreate",
			Note:       "create Pod failed: forbidden",
		},
	)
	es, err := ListWarnings(context.Background(), client, "ns", "traffic-manager")
	require.NoError(t, err)
	require.Len(t, es, 2, "the combined duplicate, the unrelated object, and the same-prefix Service must be excluded")
	names := []string{es[0].Name, es[1].Name}
	assert.Contains(t, names, "e1")
	assert.Contains(t, names, "e5")
}

func TestWriteList(t *testing.T) {
	now := meta.Now()
	es := []*events.Event{
		{
			ObjectMeta: meta.ObjectMeta{CreationTimestamp: now},
			Regarding:  core.ObjectReference{Kind: "Pod", Name: "traffic-manager-abc"},
			Type:       "Warning",
			Reason:     "Failed",
			Note:       `Failed to pull image "tel2:9.9.9"`,
		},
	}
	bf := &strings.Builder{}
	WriteList(bf, es)
	out := bf.String()

	require.Contains(t, out, "AGE")
	require.Contains(t, out, "REASON")
	require.Contains(t, out, "OBJECT")
	require.Contains(t, out, "Failed")
	require.Contains(t, out, "pod/traffic-manager-abc")
	require.Contains(t, out, `Failed to pull image "tel2:9.9.9"`)
}

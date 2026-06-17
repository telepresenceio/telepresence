package eventwatch

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	core "k8s.io/api/core/v1"
	events "k8s.io/api/events/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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

package manifest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

func TestAttachmentRPCType(t *testing.T) {
	assert.Equal(t, types.AttachmentTypeIntercept, attachmentRPCType(TypeIntercept))
	assert.Equal(t, types.AttachmentTypeWiretap, attachmentRPCType(TypeWiretap))
	assert.Equal(t, types.AttachmentTypeReplace, attachmentRPCType(TypeReplace))
	assert.Equal(t, types.AttachmentTypeIngest, attachmentRPCType(TypeIngest))
}

func TestDiffInterceptSpec(t *testing.T) {
	existing := &manager.InterceptInfo{
		Spec: &manager.InterceptSpec{
			Agent:          "echo-server",
			Namespace:      "default",
			ServiceName:    "echo-easy",
			PortIdentifier: "8080",
			Mechanism:      "tcp",
		},
		ClientMountPoint: "/tmp/echo-mounts",
	}

	t.Run("matching spec has no drift", func(t *testing.T) {
		a := &Attachment{Name: "echo-easy", Service: "echo-easy"}
		want := &manager.InterceptSpec{
			Agent:          "echo-server",
			Namespace:      "default",
			ServiceName:    "echo-easy",
			PortIdentifier: "8080",
			Mechanism:      "tcp",
		}
		assert.Empty(t, diffInterceptSpec(a, want, existing))
	})

	t.Run("unspecified fields are never drift", func(t *testing.T) {
		a := &Attachment{Name: "echo-easy"} // service/ports/container not specified
		want := &manager.InterceptSpec{
			Agent:          "echo-server",
			Namespace:      "default",
			ServiceName:    "something-else",
			PortIdentifier: "9090",
			Mechanism:      "tcp",
		}
		assert.Empty(t, diffInterceptSpec(a, want, existing))
	})

	t.Run("specified service drift is reported", func(t *testing.T) {
		a := &Attachment{Name: "echo-easy", Service: "echo-easy"}
		want := &manager.InterceptSpec{
			Agent:          "echo-server",
			Namespace:      "default",
			ServiceName:    "different-service",
			PortIdentifier: "8080",
			Mechanism:      "tcp",
		}
		diffs := diffInterceptSpec(a, want, existing)
		require.Len(t, diffs, 1)
		assert.Contains(t, diffs[0], "service")
	})

	t.Run("specified mount path drift is reported", func(t *testing.T) {
		a := &Attachment{Name: "echo-easy", Mount: &Mount{Path: "/tmp/other"}}
		want := &manager.InterceptSpec{Agent: "echo-server", Namespace: "default", Mechanism: "tcp"}
		diffs := diffInterceptSpec(a, want, existing)
		require.Len(t, diffs, 1)
		assert.Contains(t, diffs[0], "mount.path")
	})

	t.Run("specified nodeAgent drift is reported", func(t *testing.T) {
		a := &Attachment{Name: "echo-easy", NodeAgent: new(true)}
		want := &manager.InterceptSpec{Agent: "echo-server", Namespace: "default", Mechanism: "tcp", NodeAgent: true}
		notUsingNodeAgent := &manager.InterceptInfo{
			Spec: &manager.InterceptSpec{Agent: "echo-server", Namespace: "default", Mechanism: "tcp", NodeAgent: false},
		}
		diffs := diffInterceptSpec(a, want, notUsingNodeAgent)
		require.Len(t, diffs, 1)
		assert.Contains(t, diffs[0], "nodeAgent")
	})

	t.Run("mechanism drift is always reported", func(t *testing.T) {
		a := &Attachment{Name: "echo-easy"}
		want := &manager.InterceptSpec{Agent: "echo-server", Namespace: "default", Mechanism: "http"}
		diffs := diffInterceptSpec(a, want, existing)
		require.Len(t, diffs, 1)
		assert.Contains(t, diffs[0], "mechanism")
	})
}

func TestDiffIngestSpec(t *testing.T) {
	existing := &connector.IngestInfo{
		Workload:         "echo-sidecar",
		Container:        "logger",
		Namespace:        "default",
		ClientMountPoint: "/tmp/echo-mounts",
	}

	t.Run("matching spec has no drift", func(t *testing.T) {
		a := &Attachment{Name: "echo-sidecar", Container: "logger"}
		assert.Empty(t, diffIngestSpec(a, existing))
	})

	t.Run("specified container drift is reported", func(t *testing.T) {
		a := &Attachment{Name: "echo-sidecar", Container: "other"}
		diffs := diffIngestSpec(a, existing)
		require.Len(t, diffs, 1)
		assert.Contains(t, diffs[0], "container")
	})

	t.Run("specified mount path drift is reported", func(t *testing.T) {
		a := &Attachment{Name: "echo-sidecar", Mount: &Mount{Path: "/tmp/other"}}
		diffs := diffIngestSpec(a, existing)
		require.Len(t, diffs, 1)
		assert.Contains(t, diffs[0], "mount.path")
	})

	t.Run("unspecified namespace and container are never drift", func(t *testing.T) {
		a := &Attachment{Name: "echo-sidecar"}
		assert.Empty(t, diffIngestSpec(a, existing))
	})
}

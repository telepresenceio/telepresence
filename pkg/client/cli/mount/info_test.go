package mount

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/v2/pkg/types"
)

// A cluster test cannot be used: it would need a pod whose container runtime accepts such mount
// paths and a host with docker, and the only observable result is the filtered map.
func TestSafeMounts(t *testing.T) {
	in := types.MountPolicies{
		"/var/run/secrets":   types.MountPolicyRemote,
		"/x,src=/,dst=/host": types.MountPolicyRemote,
		"/../../var/run":     types.MountPolicyRemote,
		"/a/../b":            types.MountPolicyLocal,
		"relative":           types.MountPolicyRemote,
		"/a:b":               types.MountPolicyRemote,
		"/a\"b":              types.MountPolicyRemote,
		"/a\nb":              types.MountPolicyRemote,
		"/a//b":              types.MountPolicyRemote,
		"/data/":             types.MountPolicyRemote,
		"/tmp":               types.MountPolicyLocal,
	}
	out := safeMounts(context.Background(), in)
	assert.Equal(t, types.MountPolicies{
		"/var/run/secrets": types.MountPolicyRemote,
		"/data/":           types.MountPolicyRemote,
		"/tmp":             types.MountPolicyLocal,
	}, out)
}

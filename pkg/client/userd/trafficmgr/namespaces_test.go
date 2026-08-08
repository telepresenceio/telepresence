package trafficmgr

import (
	"testing"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/require"
)

// TestManagerSupportsWatchNamespaces exercises the version gate that decides whether the
// client consumes the manager's WatchNamespaces RPC or falls back to its own Kubernetes
// namespace watcher.
func TestManagerSupportsWatchNamespaces(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"older major", "1.99.0", false},
		{"older minor", "2.31.9", false},
		{"older patch", "2.32.0-alpha.0", true}, // pre-release identifiers do not affect Major/Minor/Patch comparison
		{"exact", "2.32.0", true},
		{"newer patch", "2.32.1", true},
		{"newer minor", "2.33.0", true},
		{"newer major", "3.0.0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &session{managerVersion: semver.MustParse(tt.version)}
			require.Equal(t, tt.want, s.managerSupportsWatchNamespaces())
		})
	}
}

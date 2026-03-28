package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseMacFUSEMajorVersion(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected int
		wantErr  bool
	}{
		{
			name: "macFUSE 5.x",
			output: `SSHFS version 2.10
FUSE library version: 2.9.9
macFUSE 5.0.2
fuse: no mount point`,
			expected: 5,
		},
		{
			name: "macFUSE 4.x",
			output: `SSHFS version 2.10
FUSE library version: 2.9.9
macFUSE 4.6.1
fuse: no mount point`,
			expected: 4,
		},
		{
			name: "OSXFUSE old",
			output: `SSHFS version 2.5
OSXFUSE 3.8.3
fuse: no mount point`,
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "no version found",
			output:   `SSHFS version 2.10`,
			expected: 0,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ver, err := parseMacFUSEMajorVersion([]byte(tt.output))
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, ver)
			}
		})
	}
}

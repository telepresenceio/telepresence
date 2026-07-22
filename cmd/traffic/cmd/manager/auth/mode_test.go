package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
)

func TestMode_UnmarshalText(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    auth.Mode
		wantErr bool
	}{
		{"empty defaults to permissive", "", auth.ModePermissive, false},
		{"disabled", "disabled", auth.ModeDisabled, false},
		{"permissive", "permissive", auth.ModePermissive, false},
		{"enforcing", "enforcing", auth.ModeEnforcing, false},
		{"case insensitive", "Enforcing", auth.ModeEnforcing, false},
		{"upper case", "DISABLED", auth.ModeDisabled, false},
		{"unknown value", "bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m auth.Mode
			err := m.UnmarshalText([]byte(tt.text))
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "disabled")
				assert.Contains(t, err.Error(), "permissive")
				assert.Contains(t, err.Error(), "enforcing")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, m)
		})
	}
}

package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
)

func TestGrant_UnmarshalText(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    auth.Grant
		wantErr bool
	}{
		{"empty defaults to any", "", auth.GrantAny, false},
		{"portforward", "portforward", auth.GrantPortForward, false},
		{"telepresence", "telepresence", auth.GrantTelepresence, false},
		{"any", "any", auth.GrantAny, false},
		{"case insensitive", "Telepresence", auth.GrantTelepresence, false},
		{"upper case", "PORTFORWARD", auth.GrantPortForward, false},
		{"unknown value", "bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var g auth.Grant
			err := g.UnmarshalText([]byte(tt.text))
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "portforward")
				assert.Contains(t, err.Error(), "telepresence")
				assert.Contains(t, err.Error(), "any")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, g)
		})
	}
}

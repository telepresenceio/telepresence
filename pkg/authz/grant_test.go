package authz_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/pkg/authz"
)

func TestGrant_UnmarshalText(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    authz.Grant
		wantErr bool
	}{
		{"empty defaults to any", "", authz.GrantAny, false},
		{"portforward", "portforward", authz.GrantPortForward, false},
		{"telepresence", "telepresence", authz.GrantTelepresence, false},
		{"any", "any", authz.GrantAny, false},
		{"case insensitive", "Telepresence", authz.GrantTelepresence, false},
		{"upper case", "PORTFORWARD", authz.GrantPortForward, false},
		{"unknown value", "bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var g authz.Grant
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

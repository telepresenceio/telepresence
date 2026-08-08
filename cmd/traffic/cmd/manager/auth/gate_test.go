package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
)

func TestGate_UnmarshalText(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    auth.Gate
		wantErr bool
	}{
		{"empty defaults to any", "", auth.GateAny, false},
		{"portforward", "portforward", auth.GatePortForward, false},
		{"telepresence", "telepresence", auth.GateTelepresence, false},
		{"any", "any", auth.GateAny, false},
		{"case insensitive", "Telepresence", auth.GateTelepresence, false},
		{"upper case", "PORTFORWARD", auth.GatePortForward, false},
		{"unknown value", "bogus", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var g auth.Gate
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

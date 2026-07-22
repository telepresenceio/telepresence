package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestManagerAuthError(t *testing.T) {
	tests := []struct {
		name           string
		vi             *manager.VersionInfo2
		hasTokenSource bool
		wantErr        bool
	}{
		{
			name:           "auth required, no token source",
			vi:             &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true},
			hasTokenSource: false,
			wantErr:        true,
		},
		{
			name:           "auth required, token source present",
			vi:             &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true},
			hasTokenSource: true,
			wantErr:        false,
		},
		{
			name:           "auth supported only",
			vi:             &manager.VersionInfo2{Name: "traffic-manager", AuthSupported: true},
			hasTokenSource: false,
			wantErr:        false,
		},
		{
			name:           "neither supported nor required",
			vi:             &manager.VersionInfo2{Name: "traffic-manager"},
			hasTokenSource: false,
			wantErr:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := managerAuthError(tt.vi, tt.hasTokenSource)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, "bearer token")
			require.ErrorContains(t, err, "security.authentication.mode")
		})
	}
}

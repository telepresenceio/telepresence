package k8s

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func TestManagerAuthError(t *testing.T) {
	tests := []struct {
		name             string
		vi               *manager.VersionInfo2
		hasBearerSource  bool
		hasX509Path      bool
		wantErr          bool
		wantX509Mentions bool
	}{
		{
			name:             "auth required, no bearer source, no x509 path",
			vi:               &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true},
			hasBearerSource:  false,
			hasX509Path:      false,
			wantErr:          true,
			wantX509Mentions: true,
		},
		{
			name:            "auth required, bearer source present",
			vi:              &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true},
			hasBearerSource: true,
			hasX509Path:     false,
			wantErr:         false,
		},
		{
			name:            "auth required, x509 path present",
			vi:              &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true, AuthX509Port: 15007},
			hasBearerSource: false,
			hasX509Path:     true,
			wantErr:         false,
		},
		{
			name:            "auth required, both bearer source and x509 path present",
			vi:              &manager.VersionInfo2{Name: "traffic-manager", AuthRequired: true, AuthX509Port: 15007},
			hasBearerSource: true,
			hasX509Path:     true,
			wantErr:         false,
		},
		{
			name:            "auth supported only",
			vi:              &manager.VersionInfo2{Name: "traffic-manager", AuthSupported: true},
			hasBearerSource: false,
			hasX509Path:     false,
			wantErr:         false,
		},
		{
			name:            "neither supported nor required",
			vi:              &manager.VersionInfo2{Name: "traffic-manager"},
			hasBearerSource: false,
			hasX509Path:     false,
			wantErr:         false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := managerAuthError(tt.vi, tt.hasBearerSource, tt.hasX509Path)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, "bearer token")
			require.ErrorContains(t, err, "security.authentication.mode")
			if tt.wantX509Mentions {
				require.ErrorContains(t, err, "security.authentication.x509.enabled")
			}
		})
	}
}

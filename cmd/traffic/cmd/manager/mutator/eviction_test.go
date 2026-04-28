package mutator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateRestartAnnotationPatch(t *testing.T) {
	tests := []struct {
		name         string
		annotations  map[string]string
		wantContains string
	}{
		{
			name:         "no existing annotations",
			annotations:  map[string]string{},
			wantContains: "telepresence.getambassador.io/restartedAt",
		},
		{
			name:         "existing restartedAt annotation",
			annotations:  map[string]string{"telepresence.getambassador.io/restartedAt": "old-value"},
			wantContains: "replace",
		},
		{
			name:         "other annotations present",
			annotations:  map[string]string{"other-key": "other-value"},
			wantContains: "telepresence.getambassador.io/restartedAt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			patch := generateRestartAnnotationPatch(tt.annotations)
			require.Contains(t, patch, tt.wantContains)
		})
	}
}
